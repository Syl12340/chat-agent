//go:build ignore

// Regenerate PWA icons with: go run scripts/icons.go
package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
)

func main() {
	for _, size := range []int{192, 512} {
		img := image.NewRGBA(image.Rect(0, 0, size, size))
		green, cream := color.RGBA{23, 107, 89, 255}, color.RGBA{244, 248, 233, 255}
		for y := 0; y < size; y++ {
			for x := 0; x < size; x++ {
				u, v := float64(x)*192/float64(size), float64(y)*192/float64(size)
				c := green
				// Chat pocket stays inside the maskable-icon safe region.
				if u >= 52 && u <= 140 && v >= 54 && v <= 125 || u >= 52 && u <= 116 && v >= 125 && v <= 138 || u >= 52 && u <= 77 && v > 138 && v <= 153-(u-52)*.6 {
					c = cream
				}
				if u >= 73 && u <= 119 && v >= 76 && v <= 84 || u >= 73 && u <= 106 && v >= 93 && v <= 101 {
					c = green
				}
				img.SetRGBA(x, y, c)
			}
		}
		name := fmt.Sprintf("web/icon-%d.png", size)
		file, err := os.Create(name)
		if err != nil {
			panic(err)
		}
		err = png.Encode(file, img)
		closeErr := file.Close()
		if err != nil {
			panic(err)
		}
		if closeErr != nil {
			panic(closeErr)
		}
	}
}
