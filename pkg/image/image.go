package image

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
)

// ConcatImages concatenates images in a grid
func concat(images []image.Image, rows, cols int) image.Image {
	if len(images) == 0 {
		return nil
	}

	// Assuming all images have the same size, get the dimensions of the first image
	imgWidth := images[0].Bounds().Dx()
	imgHeight := images[0].Bounds().Dy()

	// Create a blank canvas for the final image
	gridWidth := cols * imgWidth
	gridHeight := rows * imgHeight
	newImage := image.NewRGBA(image.Rect(0, 0, gridWidth, gridHeight))

	// Fill the background with white color (optional)
	draw.Draw(newImage, newImage.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)

	// Draw each image in its respective place on the grid
	for idx, img := range images {
		xOffset := (idx % cols) * imgWidth
		yOffset := (idx / cols) * imgHeight
		r := image.Rect(xOffset, yOffset, xOffset+imgWidth, yOffset+imgHeight)
		draw.Draw(newImage, r, img, image.Point{}, draw.Src)
	}

	return newImage
}

func decode(b []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	return img, nil
}

func encode(i image.Image) ([]byte, error) {
	w := &bytes.Buffer{}
	err := jpeg.Encode(w, i, &jpeg.Options{Quality: 100})
	if err != nil {
		return nil, fmt.Errorf("encode image: %w", err)
	}

	return w.Bytes(), nil
}

func Concat(images [][]byte, rows, cols int) ([]byte, error) {
	imgs := make([]image.Image, len(images))
	for i := range images {
		img, err := decode(images[i])
		if err != nil {
			return nil, fmt.Errorf("concat images: %w", err)
		}
		imgs[i] = img
	}

	collage := concat(imgs, rows, cols)
	return encode(collage)
}
