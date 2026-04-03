package main

import (
	"flag"
	"fmt"
	"log"
)

func main() {
	host := flag.String("host", "127.0.0.1", "服务器地址")
	port := flag.Int("port", 10096, "端口号")
	audioIn := flag.String("audio_in", "speaker123.wav", "输入音频文件路径")
	flag.Parse()

	if *audioIn == "" {
		log.Fatal("必须指定 audio_in 参数")
	}

	transcriber := NewAudioTranscriber(*host, *port, *audioIn)

	ret := transcriber.Run()
	for _, result := range ret {
		// 解析 sentences 字段（包含 speaker 信息）
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

		// 输出完整文本
		if text, ok := result["text"].(string); ok {
			fmt.Println("\n========== 完整文本 ==========")
			fmt.Println(text)
		}
	}

	//fmt.Println(ret)
}
