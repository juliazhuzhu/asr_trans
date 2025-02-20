package main

import (
	"flag"
	"fmt"
	"log"
)

func main() {
	host := flag.String("host", "172.20.0.103", "服务器地址")
	port := flag.Int("port", 10098, "端口号")
	audioIn := flag.String("audio_in", "output.wav", "输入音频文件路径")
	flag.Parse()

	if *audioIn == "" {
		log.Fatal("必须指定 audio_in 参数")
	}

	transcriber := NewAudioTranscriber(*host, *port, *audioIn)
	ret := transcriber.Run()
	for _, result := range ret {
		if stamps, ok := result["stamp_sents"].([]interface{}); ok {
			for _, val := range stamps {
				//iterate every sentence.
				if valMap, ok := val.(map[string]interface{}); ok {
					fmt.Printf("start %v, end %v, text:%s\n", valMap["start"], valMap["end"], valMap["text_seg"])
				}

			}
			//get whole chapter
			fmt.Println(result["text"])
		}
	}

	//fmt.Println(ret)
}
