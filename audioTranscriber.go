package main

import (
	"bytes"
	"context"
	"crypto/tls"

	//"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

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
	wavs, err := at.splitWavFile(at.AudioIn, 10, 10, 60000)
	if err != nil {
		return nil, fmt.Errorf("split wav file failed: %w", err)
	}

	// 生成 session ID 用于跨片段说话人一致性
	if at.SessionID == "" {
		at.SessionID = fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	log.Printf("Session ID: %s", at.SessionID)

	resultChan := make(chan map[string]interface{}, len(wavs))
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Done()
	offset := 3600000
	for i, wavPath := range wavs {
		if err := at.wsClient(ctx, i, wavPath, resultChan, float64(i*offset)); err != nil {
			return nil, fmt.Errorf("wsClient(%d) failed: %w", i, err)
		}

	}

	// 所有片段发送完成后，发送 session_end 消息
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
	return results, nil
}

// 捕获退出信号
func (at *AudioTranscriber) setupSignalHandler(cancel context.CancelFunc) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
		at.cleanupSplitFiles()
		os.Exit(1)
	}()
}

// 删除临时文件
func (at *AudioTranscriber) cleanupSplitFiles() {
	at.mu.Lock()
	defer at.mu.Unlock()
	for _, file := range at.SplitFiles {
		if err := os.Remove(file); err != nil {
			log.Printf("删除文件失败 %s: %v\n", file, err)
		} else {
			log.Printf("已删除临时文件: %s\n", file)
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
		return nil, err
	}
	defer file.Close()

	var header [44]byte // Simplified assumption for a WAV header size
	if _, err := file.Read(header[:]); err != nil {
		return nil, err
	}

	// Read the sample rate from the header (bytes 24-27)
	var sampleRate uint32
	binary.Read(bytes.NewReader(header[24:28]), binary.LittleEndian, &sampleRate)

	// For simplicity, assume we know the format and skip directly to reading audio data
	audioBytes, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	fmt.Println("len audio bytes", len(audioBytes))
	//stride := int(60 * 10 * sampleRate * 2 / chunkInterval / 1000) // Simplified calculation for stride
	// 假设 chunkSize 是 int16 类型，sampleRate 是 uint32 类型
	stride := int(float64(60) * float64(chunkSize) * float64(sampleRate) * 2.0 / float64(chunkInterval) / 1000.0)
	chunkNum := (len(audioBytes)-1)/stride + 1

	wavs := []string{}
	if chunkNum > maxChunks {
		maxBytesPerChunk := maxChunks * stride
		numFiles := (len(audioBytes)-1)/maxBytesPerChunk + 1

		for i := 0; i < numFiles; i++ {
			start := i * maxBytesPerChunk
			end := min((i+1)*maxBytesPerChunk, len(audioBytes))
			chunkData := audioBytes[start:end]

			outputPath := fmt.Sprintf("%s_part%d.wav", removeExt(wavPath), i+1)
			newFile, err := os.Create(outputPath)
			if err != nil {
				return wavs, err
			}
			defer newFile.Close()

			// Write the header first
			if _, err := newFile.Write(header[:]); err != nil {
				return wavs, err
			}

			// Then write the chunk data
			if _, err := newFile.Write(chunkData); err != nil {
				return wavs, err
			}
			wavs = append(wavs, outputPath)
			at.addSplitFile(outputPath)
			fmt.Printf("Created %s\n", outputPath)
		}
	} else {
		fmt.Println("No need to split the file.")
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

	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Printf("session_end: 连接失败: %v", err)
		return
	}
	defer c.Close()

	endMsg, _ := json.Marshal(map[string]interface{}{
		"session_end": true,
		"session_id":  at.SessionID,
	})
	if err := c.WriteMessage(websocket.TextMessage, endMsg); err != nil {
		log.Printf("session_end: 发送失败: %v", err)
		return
	}

	// 等待服务端确认
	_, msg, err := c.ReadMessage()
	if err != nil {
		log.Printf("session_end: 读取响应失败: %v", err)
		return
	}
	log.Printf("session_end: 服务端响应: %s", string(msg))
}

// getCachedResult 重连服务端查询缓存结果，每 30 秒重试，总超时 30 分钟
func (at *AudioTranscriber) getCachedResult(wavPath string, timeout time.Duration) (map[string]interface{}, error) {
	u := url.URL{Scheme: "wss", Host: fmt.Sprintf("%s:%d", at.Host, at.Port), Path: "/"}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			log.Printf("getCachedResult: 连接失败: %v，30秒后重试", err)
			time.Sleep(30 * time.Second)
			continue
		}

		reqMsg, _ := json.Marshal(map[string]interface{}{
			"get_result": true,
			"session_id": at.SessionID,
			"wav_name":   wavPath,
		})
		if err := c.WriteMessage(websocket.TextMessage, reqMsg); err != nil {
			log.Printf("getCachedResult: 发送失败: %v，30秒后重试", err)
			c.Close()
			time.Sleep(30 * time.Second)
			continue
		}

		// 设置读取超时，避免无限阻塞
		c.SetReadDeadline(time.Now().Add(35 * time.Second))
		_, msg, err := c.ReadMessage()
		c.Close()
		if err != nil {
			log.Printf("getCachedResult: 读取失败: %v，30秒后重试", err)
			time.Sleep(30 * time.Second)
			continue
		}

		var asrResult ASRResult
		if err := json.Unmarshal(msg, &asrResult); err != nil {
			log.Printf("getCachedResult: 解析失败: %v，30秒后重试", err)
			time.Sleep(30 * time.Second)
			continue
		}

		if asrResult.IsFinal == "True" {
			log.Printf("getCachedResult: 成功获取缓存结果")
			resultBytes, _ := json.Marshal(asrResult)
			var result map[string]interface{}
			json.Unmarshal(resultBytes, &result)
			return result, nil
		}

		// status == "not_found"，服务端还在处理或未收到请求
		log.Printf("getCachedResult: 结果未就绪，30秒后重试")
		time.Sleep(30 * time.Second)
	}

	return nil, fmt.Errorf("getCachedResult: 超时 (%v)", timeout)
}

// WebSocket客户端
func (at *AudioTranscriber) wsClient(ctx context.Context, id int, wavPath string, resultChan chan<- map[string]interface{}, offset float64) error {
	u := url.URL{Scheme: "wss", Host: fmt.Sprintf("%s:%d", at.Host, at.Port), Path: "/"}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		resultChan <- nil
		return fmt.Errorf("连接失败: %w", err)
	}
	defer c.Close()

	done := make(chan struct{})
	connFailed := false // 标记是否因连接断开而退出

	// 接收消息
	go func() {
		defer func() { done <- struct{}{} }() // 保证退出时总是通知 wsClient
		for {

			select {
			case <-ctx.Done():
				resultChan <- nil
				return
			default:
				c.SetReadDeadline(time.Now().Add(20 * time.Minute))
				_, msg, err := c.ReadMessage()
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						log.Printf("wsClient(%d): ReadMessage 超时(20分钟)，进入兜底取结果", id)
					} else {
						log.Println("读取消息失败:", err)
					}
					connFailed = true
					resultChan <- nil
					return
				}
				c.SetReadDeadline(time.Time{})
				var asrResult ASRResult
				if err := json.Unmarshal(msg, &asrResult); err != nil {
					log.Println("解析JSON失败:", err)
					resultChan <- nil
					return
				}

				// 心跳/状态消息，继续等待
				if asrResult.IsFinal != "True" {
					continue
				}

				// 调整 stamp_sents 中的时间戳
				for i := range asrResult.StampSents {
					asrResult.StampSents[i].Start += offset
					asrResult.StampSents[i].End += offset
					// 调整 ts_list 中的时间戳
					for j := range asrResult.StampSents[i].TsList {
						asrResult.StampSents[i].TsList[j][0] += offset
						asrResult.StampSents[i].TsList[j][1] += offset
					}
				}

				// 调整 sentences 中的时间戳
				for i := range asrResult.Sentences {
					asrResult.Sentences[i].Start += offset
					asrResult.Sentences[i].End += offset
					// 调整 timestamp 中的时间戳
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

				// 转换为 map 返回
				resultBytes, _ := json.Marshal(asrResult)
				var result map[string]interface{}
				json.Unmarshal(resultBytes, &result)

				resultChan <- result
				log.Println("done part.")
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
	fmt.Printf("send: %s", configMsg)
	if err := c.WriteMessage(websocket.TextMessage, configMsg); err != nil {
		return fmt.Errorf("发送配置失败: %w", err)
	}

	// 发送音频数据
	file, _ := os.Open(wavPath)
	defer file.Close()

	audioBytes, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("读取音频文件失败: %w", err)
	}
	//fmt.Println("len audio bytes", len(audioBytes))
	//stride := int(60 * 10 * sampleRate * 2 / chunkInterval / 1000) // Simplified calculation for stride
	// 假设 chunkSize 是 int16 类型，sampleRate 是 uint32 类型
	stride := int(float64(600) * float64(config.ChunkSize[1]) * float64(config.AudioFS) * 2.0 / float64(config.ChunkInterval) / 1000.0)
	chunkNum := (len(audioBytes)-1)/stride + 1
	//fmt.Printf("chunkNum %d \n", chunkNum)
	//chunkSize := int(16000 / 1000 * 10)
	//chunkSize := stride
	//chunkSize := int(16000 / 1000 * 10)
	for i := 0; i < chunkNum; i++ {
		select {
		case <-ctx.Done():
			return nil
		default:
			beg := i * stride
			//data := audio_bytes[beg : beg+stride]
			end := beg + stride
			if end > len(audioBytes) {
				end = len(audioBytes)
			}
			// 转换为字节流
			//audioChunk := audioBytes[beg:end]
			//fmt.Printf("send begin %d, end %d \n", beg, end)
			if err := c.WriteMessage(websocket.BinaryMessage, audioBytes[beg:end]); err != nil {
				return fmt.Errorf("发送音频失败: %w", err)
			}

			time.Sleep(1 * time.Microsecond)
		}
	}

	// 发送结束标记
	end := EndMessage{
		IsSpeaking: false,
	}
	endMsg, _ := json.Marshal(end)
	log.Println("end speaking.")
	if err := c.WriteMessage(websocket.TextMessage, endMsg); err != nil {
		return fmt.Errorf("发送结束标记失败: %w", err)
	}

	<-done

	// 连接断开后尝试从服务端缓存获取结果
	if connFailed {
		log.Printf("连接断开，尝试从服务端缓存获取结果: %s", wavPath)
		result, err := at.getCachedResult(wavPath, 30*time.Minute)
		if err != nil {
			log.Printf("getCachedResult 失败: %v", err)
		} else {
			resultChan <- result
			log.Printf("getCachedResult 成功")
		}

	}

	return nil
}
