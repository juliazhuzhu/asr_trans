package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"asr_trans/pkg/log"

	"github.com/gorilla/websocket"
)

// AudioTranscriber 音频转写客户端
type AudioTranscriber struct {
	Host       string
	Port       int
	AudioIn    string
	SplitFiles []string // 记录新增的分割文件路径
	SessionID  string   // 会话 ID，用于跨片段说话人一致性
	mu         sync.Mutex
}

// Message WebSocket 消息结构
type Message struct {
	Mode          string `json:"mode"`
	ChunkSize     []int  `json:"chunk_size"`
	ChunkInterval int    `json:"chunk_interval"`
	WavName       string `json:"wav_name"`
	IsSpeaking    bool   `json:"is_speaking"`
	Hotwords      string `json:"hotwords"`
	Itn           bool   `json:"itn"`
	AudioFS       int    `json:"audio_fs"`
	WavFormat     string `json:"wav_format"`
	SessionID     string `json:"session_id,omitempty"`
}

type EndMessage struct {
	IsSpeaking bool `json:"is_speaking"`
}

// Sentence 带说话人信息的句子
type Sentence struct {
	Text      string      `json:"text"`
	Start     float64     `json:"start"`
	End       float64     `json:"end"`
	Speaker   string      `json:"speaker"`
	Timestamp [][]float64 `json:"timestamp"`
}

// StampSent 时间戳句子信息
type StampSent struct {
	TextSeg string      `json:"text_seg"`
	Punc    string      `json:"punc"`
	Start   float64     `json:"start"`
	End     float64     `json:"end"`
	TsList  [][]float64 `json:"ts_list"`
}

// ASRResult ASR识别结果
type ASRResult struct {
	IsFinal    string      `json:"is_final"`
	Status     string      `json:"status,omitempty"`
	Text       string      `json:"text"`
	Timestamp  [][]float64 `json:"timestamp"`
	StampSents []StampSent `json:"stamp_sents"`
	Sentences  []Sentence  `json:"sentences"`
}

// NewAudioTranscriber 创建一个新的音频转写客户端
func NewAudioTranscriber(host string, port int, audioIn string) *AudioTranscriber {
	return &AudioTranscriber{
		Host:    host,
		Port:    port,
		AudioIn: audioIn,
	}
}

// Run 启动音频转写
func (at *AudioTranscriber) Run() ([]map[string]interface{}, error) {
	if at.AudioIn == "" {
		return nil, fmt.Errorf("audio_in is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at.setupSignalHandler(cancel)

	log.Infof("开始分割音频文件: %s", at.AudioIn)
	wavs, err := at.splitWavFile(at.AudioIn, 10, 10, 60000)
	if err != nil {
		log.Errorf("分割音频文件失败: %v", err)
		return nil, fmt.Errorf("split wav file failed: %w", err)
	}
	log.Infof("音频分割完成, 共 %d 个片段", len(wavs))

	// 生成 session ID 用于跨片段说话人一致性
	if at.SessionID == "" {
		at.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	log.Infof("Session ID: %s", at.SessionID)

	resultChan := make(chan map[string]interface{}, len(wavs))
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Done()
	offset := 3600000
	for i, wavPath := range wavs {
		log.Infof("开始处理第 %d 个片段: %s, offset=%.0f", i, wavPath, float64(i*offset))
		if err := at.wsClient(ctx, i, wavPath, resultChan, float64(i*offset)); err != nil {
			log.Errorf("wsClient(%d) 处理失败: %v", i, err)
			return nil, fmt.Errorf("wsClient(%d) failed: %w", i, err)
		}
		log.Infof("wsClient(%d) 完成", i)
	}

	// 所有片段发送完成后，发送 session_end 消息
	log.Info("所有片段处理完成，发送 session_end 消息")
	at.sendSessionEnd()

	close(resultChan)
	at.cleanupSplitFiles()
	var results []map[string]interface{}
	for res := range resultChan {
		if res != nil {
			results = append(results, res)
		}
	}
	go func() {
		wg.Wait()
	}()

	log.Infof("转写结果收集完成, 共 %d 条结果", len(results))
	return results, nil
}

// 捕获退出信号
func (at *AudioTranscriber) setupSignalHandler(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Warnf("收到退出信号: %v, 开始清理", sig)
		cancel()
		at.cleanupSplitFiles()
		os.Exit(1)
	}()
}

// 删除临时文件
func (at *AudioTranscriber) cleanupSplitFiles() {
	at.mu.Lock()
	defer at.mu.Unlock()
	log.Infof("开始清理临时文件, 共 %d 个", len(at.SplitFiles))
	for _, file := range at.SplitFiles {
		if err := os.Remove(file); err != nil {
			log.Warnf("删除文件失败 %s: %v", file, err)
		} else {
			log.Infof("已删除临时文件: %s", file)
		}
	}
}

// 记录分割文件
func (at *AudioTranscriber) addSplitFile(file string) {
	at.mu.Lock()
	defer at.mu.Unlock()
	at.SplitFiles = append(at.SplitFiles, file)
}

func (at *AudioTranscriber) splitWavFile(wavPath string, chunkSize int16, chunkInterval int, maxChunks int) ([]string, error) {
	file, err := os.Open(wavPath)
	if err != nil {
		log.Errorf("打开音频文件失败: %s, err=%v", wavPath, err)
		return nil, err
	}
	defer file.Close()

	var header [44]byte
	if _, err := file.Read(header[:]); err != nil {
		log.Errorf("读取 WAV header 失败: %v", err)
		return nil, err
	}

	var sampleRate uint32
	binary.Read(bytes.NewReader(header[24:28]), binary.LittleEndian, &sampleRate)
	log.Infof("WAV 采样率: %d", sampleRate)

	audioBytes, err := io.ReadAll(file)
	if err != nil {
		log.Errorf("读取音频数据失败: %v", err)
		return nil, err
	}
	log.Infof("音频数据大小: %d bytes", len(audioBytes))

	stride := int(float64(60) * float64(chunkSize) * float64(sampleRate) * 2.0 / float64(chunkInterval) / 1000.0)
	chunkNum := (len(audioBytes)-1)/stride + 1
	log.Infof("stride=%d, chunkNum=%d, maxChunks=%d", stride, chunkNum, maxChunks)

	wavs := []string{}
	if chunkNum > maxChunks {
		maxBytesPerChunk := maxChunks * stride
		numFiles := (len(audioBytes)-1)/maxBytesPerChunk + 1
		log.Infof("需要分割为 %d 个文件", numFiles)

		for i := 0; i < numFiles; i++ {
			start := i * maxBytesPerChunk
			end := min((i+1)*maxBytesPerChunk, len(audioBytes))
			chunkData := audioBytes[start:end]

			outputPath := fmt.Sprintf("%s_part%d.wav", removeExt(wavPath), i+1)
			newFile, err := os.Create(outputPath)
			if err != nil {
				log.Errorf("创建分割文件失败: %s, err=%v", outputPath, err)
				return wavs, err
			}
			defer newFile.Close()

			if _, err := newFile.Write(header[:]); err != nil {
				log.Errorf("写入 header 失败: %s, err=%v", outputPath, err)
				return wavs, err
			}

			if _, err := newFile.Write(chunkData); err != nil {
				log.Errorf("写入音频数据失败: %s, err=%v", outputPath, err)
				return wavs, err
			}
			wavs = append(wavs, outputPath)
			at.addSplitFile(outputPath)
			log.Infof("创建分割文件: %s, 大小=%d bytes", outputPath, len(chunkData)+44)
		}
	} else {
		log.Info("无需分割文件")
		wavs = append(wavs, wavPath)
	}
	return wavs, nil
}

func removeExt(filename string) string {
	return filename[:len(filename)-len(".wav")]
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// sendSessionEnd 发送 session 结束消息，通知服务端清理说话人注册表
func (at *AudioTranscriber) sendSessionEnd() {
	u := url.URL{Scheme: "wss", Host: fmt.Sprintf("%s:%d", at.Host, at.Port), Path: "/"}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	log.Infof("session_end: 连接 %s", u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Errorf("session_end: 连接失败: %v", err)
		return
	}
	defer c.Close()

	endMsg, _ := json.Marshal(map[string]interface{}{
		"session_end": true,
		"session_id":  at.SessionID,
	})
	log.Infof("session_end: 发送消息: %s", string(endMsg))
	if err := c.WriteMessage(websocket.TextMessage, endMsg); err != nil {
		log.Errorf("session_end: 发送失败: %v", err)
		return
	}

	// 等待服务端确认
	_, msg, err := c.ReadMessage()
	if err != nil {
		log.Errorf("session_end: 读取响应失败: %v", err)
		return
	}
	log.Infof("session_end: 服务端响应: %s", string(msg))
}

// getCachedResult 重连服务端查询缓存结果，每 30 秒重试，总超时 30 分钟
func (at *AudioTranscriber) getCachedResult(wavPath string, timeout time.Duration) (map[string]interface{}, error) {
	u := url.URL{Scheme: "wss", Host: fmt.Sprintf("%s:%d", at.Host, at.Port), Path: "/"}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	deadline := time.Now().Add(timeout)
	attempt := 0
	for time.Now().Before(deadline) {
		attempt++
		log.Infof("getCachedResult: 第 %d 次尝试, wavPath=%s", attempt, wavPath)

		c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			log.Warnf("getCachedResult: 连接失败: %v，30秒后重试", err)
			time.Sleep(30 * time.Second)
			continue
		}

		reqMsg, _ := json.Marshal(map[string]interface{}{
			"get_result": true,
			"session_id": at.SessionID,
			"wav_name":   wavPath,
		})
		if err := c.WriteMessage(websocket.TextMessage, reqMsg); err != nil {
			log.Warnf("getCachedResult: 发送失败: %v，30秒后重试", err)
			c.Close()
			time.Sleep(30 * time.Second)
			continue
		}

		c.SetReadDeadline(time.Now().Add(35 * time.Second))
		_, msg, err := c.ReadMessage()
		c.Close()
		if err != nil {
			log.Warnf("getCachedResult: 读取失败: %v，30秒后重试", err)
			time.Sleep(30 * time.Second)
			continue
		}

		var asrResult ASRResult
		if err := json.Unmarshal(msg, &asrResult); err != nil {
			log.Warnf("getCachedResult: 解析失败: %v，30秒后重试", err)
			time.Sleep(30 * time.Second)
			continue
		}

		if asrResult.IsFinal == "True" {
			log.Infof("getCachedResult: 成功获取缓存结果, text长度=%d", len(asrResult.Text))
			resultBytes, _ := json.Marshal(asrResult)
			var result map[string]interface{}
			json.Unmarshal(resultBytes, &result)
			return result, nil
		}

		log.Infof("getCachedResult: 结果未就绪 (status=%s)，30秒后重试", asrResult.Status)
		time.Sleep(30 * time.Second)
	}

	log.Errorf("getCachedResult: 超时 (%v)", timeout)
	return nil, fmt.Errorf("getCachedResult: 超时 (%v)", timeout)
}

// WebSocket客户端
func (at *AudioTranscriber) wsClient(ctx context.Context, id int, wavPath string, resultChan chan<- map[string]interface{}, offset float64) error {
	u := url.URL{Scheme: "wss", Host: fmt.Sprintf("%s:%d", at.Host, at.Port), Path: "/"}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	log.Infof("wsClient(%d): 连接 %s", id, u.String())
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Errorf("wsClient(%d): 连接失败: %v", id, err)
		resultChan <- nil
		return fmt.Errorf("连接失败: %w", err)
	}
	defer c.Close()

	done := make(chan struct{})
	connFailed := false

	// 接收消息
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			select {
			case <-ctx.Done():
				log.Infof("wsClient(%d): context 取消，退出接收", id)
				resultChan <- nil
				return
			default:
				c.SetReadDeadline(time.Now().Add(20 * time.Minute))
				_, msg, err := c.ReadMessage()
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						log.Warnf("wsClient(%d): ReadMessage 超时(20分钟)，进入兜底取结果", id)
					} else {
						log.Errorf("wsClient(%d): 读取消息失败: %v", id, err)
					}
					connFailed = true
					resultChan <- nil
					return
				}
				c.SetReadDeadline(time.Time{})

				var asrResult ASRResult
				if err := json.Unmarshal(msg, &asrResult); err != nil {
					log.Errorf("wsClient(%d): 解析JSON失败: %v, raw=%s", id, err, string(msg))
					resultChan <- nil
					return
				}

				// 心跳/状态消息，继续等待
				if asrResult.IsFinal != "True" {
					log.Debugf("wsClient(%d): 收到中间结果, is_final=%s, status=%s", id, asrResult.IsFinal, asrResult.Status)
					continue
				}

				log.Infof("wsClient(%d): 收到最终结果, sentences=%d, stamp_sents=%d", id, len(asrResult.Sentences), len(asrResult.StampSents))

				// 调整 stamp_sents 中的时间戳
				for i := range asrResult.StampSents {
					asrResult.StampSents[i].Start += offset
					asrResult.StampSents[i].End += offset
					for j := range asrResult.StampSents[i].TsList {
						asrResult.StampSents[i].TsList[j][0] += offset
						asrResult.StampSents[i].TsList[j][1] += offset
					}
				}

				// 调整 sentences 中的时间戳
				for i := range asrResult.Sentences {
					asrResult.Sentences[i].Start += offset
					asrResult.Sentences[i].End += offset
					for j := range asrResult.Sentences[i].Timestamp {
						asrResult.Sentences[i].Timestamp[j][0] += offset
						asrResult.Sentences[i].Timestamp[j][1] += offset
					}
				}

				// 调整顶层 timestamp
				for i := range asrResult.Timestamp {
					asrResult.Timestamp[i][0] += offset
					asrResult.Timestamp[i][1] += offset
				}

				resultBytes, _ := json.Marshal(asrResult)
				var result map[string]interface{}
				json.Unmarshal(resultBytes, &result)

				resultChan <- result
				log.Infof("wsClient(%d): 片段处理完成", id)
				return
			}
		}
	}()

	// 发送配置消息
	config := Message{
		Mode:          "offline",
		ChunkSize:     []int{5, 10, 5},
		ChunkInterval: 10,
		WavName:       wavPath,
		IsSpeaking:    true,
		Hotwords:      "",
		Itn:           true,
		AudioFS:       16000,
		WavFormat:     "pcm",
		SessionID:     at.SessionID,
	}

	configMsg, _ := json.Marshal(config)
	log.Infof("wsClient(%d): 发送配置: %s", id, string(configMsg))
	if err := c.WriteMessage(websocket.TextMessage, configMsg); err != nil {
		log.Errorf("wsClient(%d): 发送配置失败: %v", id, err)
		return fmt.Errorf("发送配置失败: %w", err)
	}

	// 发送音频数据
	file, err := os.Open(wavPath)
	if err != nil {
		log.Errorf("wsClient(%d): 打开音频文件失败: %s, err=%v", id, wavPath, err)
		return fmt.Errorf("打开音频文件失败: %w", err)
	}
	defer file.Close()

	audioBytes, err := io.ReadAll(file)
	if err != nil {
		log.Errorf("wsClient(%d): 读取音频文件失败: %v", id, err)
		return fmt.Errorf("读取音频文件失败: %w", err)
	}

	stride := int(float64(600) * float64(config.ChunkSize[1]) * float64(config.AudioFS) * 2.0 / float64(config.ChunkInterval) / 1000.0)
	chunkNum := (len(audioBytes)-1)/stride + 1
	log.Infof("wsClient(%d): 音频数据=%d bytes, stride=%d, chunkNum=%d", id, len(audioBytes), stride, chunkNum)

	for i := 0; i < chunkNum; i++ {
		select {
		case <-ctx.Done():
			log.Infof("wsClient(%d): context 取消，停止发送音频", id)
			return nil
		default:
			beg := i * stride
			end := beg + stride
			if end > len(audioBytes) {
				end = len(audioBytes)
			}
			if err := c.WriteMessage(websocket.BinaryMessage, audioBytes[beg:end]); err != nil {
				log.Errorf("wsClient(%d): 发送音频 chunk %d 失败: %v", id, i, err)
				return fmt.Errorf("发送音频失败: %w", err)
			}

			time.Sleep(1 * time.Microsecond)
		}
	}
	log.Infof("wsClient(%d): 音频数据发送完成, 共 %d 个 chunk", id, chunkNum)

	// 发送结束标记
	end := EndMessage{
		IsSpeaking: false,
	}
	endMsg, _ := json.Marshal(end)
	log.Infof("wsClient(%d): 发送结束标记", id)
	if err := c.WriteMessage(websocket.TextMessage, endMsg); err != nil {
		log.Errorf("wsClient(%d): 发送结束标记失败: %v", id, err)
		return fmt.Errorf("发送结束标记失败: %w", err)
	}

	<-done

	// 连接断开后尝试从服务端缓存获取结果
	if connFailed {
		log.Warnf("wsClient(%d): 连接断开，尝试从服务端缓存获取结果: %s", id, wavPath)
		result, err := at.getCachedResult(wavPath, 30*time.Minute)
		if err != nil {
			log.Errorf("wsClient(%d): getCachedResult 失败: %v", id, err)
		} else {
			resultChan <- result
			log.Infof("wsClient(%d): getCachedResult 成功", id)
		}
	}

	return nil
}
