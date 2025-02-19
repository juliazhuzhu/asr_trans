package main

import (
	"flag"
	"fmt"
	"log"
)

// intBufferToBytes 转换音频缓冲区为字节流
func intBufferToBytes(data []int, bytesPerSample int) []byte {
	buf := make([]byte, len(data)*bytesPerSample)
	for i, v := range data {
		for b := 0; b < bytesPerSample; b++ {
			buf[i*bytesPerSample+b] = byte(v >> (b * 8))
		}
	}
	return buf
}

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
	fmt.Println(ret)
}
