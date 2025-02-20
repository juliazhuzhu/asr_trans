package main

import (
	"flag"
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
	transcriber.Run()
	//fmt.Println(ret)
}
