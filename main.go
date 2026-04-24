package main

import (
	"flag"
	"fmt"
	"os"

	"asr_trans/pkg/log"
)

func initLogger() {
	cfg := &log.Config{
		Writers:          "stdout,file",
		LoggerLevel:      "debug",
		LoggerFile:       "./logs/asr_trans.log",
		LoggerWarnFile:   "./logs/asr_trans.wf.log",
		LoggerErrorFile:  "./logs/asr_trans.err.log",
		LogFormatText:    false,
		LogRollingPolicy: "daily",
		LogRotateDate:    1,
		LogRotateSize:    1,
		LogBackupCount:   7,
		AppName:          "asr_trans",
	}
	if err := log.NewLogger(cfg, log.InstanceZapLogger); err != nil {
		fmt.Fprintf(os.Stderr, "init logger failed: %v\n", err)
		os.Exit(1)
	}
}

func main() {
	host := flag.String("host", "127.0.0.1", "服务器地址")
	port := flag.Int("port", 10096, "端口号")
	audioIn := flag.String("audio_in", "speaker123.wav", "输入音频文件路径")
	flag.Parse()

	initLogger()

	if *audioIn == "" {
		log.Error("必须指定 audio_in 参数")
		os.Exit(1)
	}

	log.Infof("启动转写, host=%s, port=%d, audio_in=%s", *host, *port, *audioIn)

	transcriber := NewAudioTranscriber(*host, *port, *audioIn)

	ret, err := transcriber.Run()
	if err != nil {
		log.Errorf("转写失败: %v", err)
		os.Exit(1)
	}

	for _, result := range ret {
		if sentences, ok := result["sentences"].([]interface{}); ok {
			fmt.Println("\n========== 识别结果（带说话人） ==========")
			for _, val := range sentences {
				if valMap, ok := val.(map[string]interface{}); ok {
					speaker := valMap["speaker"]
					text := valMap["text"]
					start := valMap["start"]
					end := valMap["end"]
					fmt.Printf("[%s] %.0fms - %.0fms: %s\n", speaker, start, end, text)
				}
			}
		}

		if text, ok := result["text"].(string); ok {
			fmt.Println("\n========== 完整文本 ==========")
			fmt.Println(text)
		}
	}

	log.Info("转写完成")
}
