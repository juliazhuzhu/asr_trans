package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"strings"
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
}

type EndMessage struct {
	IsSpeaking bool `json:"is_speaking"`
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
func (at *AudioTranscriber) Run() string {
	if at.AudioIn == "" {
		log.Fatal("audio_in is required")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at.setupSignalHandler(cancel)
	wavs, _ := at.splitWavFile(at.AudioIn, 10, 10, 60000)

	resultChan := make(chan string, len(wavs))
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Done()
	for i, wavPath := range wavs {
		at.wsClient(ctx, i, wavPath, resultChan)
	}
	close(resultChan)
	at.cleanupSplitFiles()
	var results []string
	for res := range resultChan {
		results = append(results, res)
	}
	go func() {
		wg.Wait()

	}()
	return strings.Join(results, " ")
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

// WebSocket客户端
func (at *AudioTranscriber) wsClient(ctx context.Context, id int, wavPath string, resultChan chan<- string) {
	u := url.URL{Scheme: "wss", Host: fmt.Sprintf("%s:%d", at.Host, at.Port), Path: "/"}
	websocket.DefaultDialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		resultChan <- ""
		log.Fatal("连接失败:", err)
	}
	defer c.Close()

	done := make(chan struct{})

	// 接收消息
	go func() {
		//defer close(done)
		for {
			select {
			case <-ctx.Done():
				done <- struct{}{}
				resultChan <- ""
				return
			default:
				_, msg, err := c.ReadMessage()
				if err != nil {
					log.Println("读取消息失败:", err)
					return
				}
				var result map[string]interface{}
				if err := json.Unmarshal(msg, &result); err != nil {
					log.Println("解析JSON失败:", err)
					done <- struct{}{}
					resultChan <- ""
					return
				}
				if text, ok := result["text"].(string); ok {
					resultChan <- text
					log.Println("done part.")
				}
				done <- struct{}{}
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
	}

	configMsg, _ := json.Marshal(config)
	fmt.Printf("send: %s", configMsg)
	if err := c.WriteMessage(websocket.TextMessage, configMsg); err != nil {
		log.Fatal("发送配置失败:", err)
	}

	// 发送音频数据
	file, _ := os.Open(wavPath)
	defer file.Close()

	audioBytes, err := io.ReadAll(file)
	if err != nil {
		return
	}
	fmt.Println("len audio bytes", len(audioBytes))
	//stride := int(60 * 10 * sampleRate * 2 / chunkInterval / 1000) // Simplified calculation for stride
	// 假设 chunkSize 是 int16 类型，sampleRate 是 uint32 类型
	stride := int(float64(60) * float64(config.ChunkSize[1]) * float64(config.AudioFS) * 2.0 / float64(config.ChunkInterval) / 1000.0)
	chunkNum := (len(audioBytes)-1)/stride + 1
	fmt.Printf("chunkNum %d \n", chunkNum)
	//chunkSize := int(16000 / 1000 * 10)
	//chunkSize := stride
	//chunkSize := int(16000 / 1000 * 10)
	for i := 0; i < chunkNum; i++ {
		select {
		case <-ctx.Done():
			return
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
				log.Fatal("发送音频失败:", err)
			}

			time.Sleep(1 * time.Microsecond)
		}
	}

	// 发送结束标记
	end := EndMessage{
		IsSpeaking: false,
	}
	endMsg, _ := json.Marshal(end)
	fmt.Printf("end speaking. \n")
	if err := c.WriteMessage(websocket.TextMessage, endMsg); err != nil {
		log.Fatal("发送结束标记失败:", err)
	}

	<-done
}
