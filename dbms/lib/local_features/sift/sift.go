package sift

import (
	"fmt"
	"image/color"

	"gocv.io/x/gocv"
)

func ReadImage(path string) (gocv.Mat, error) {
	img := gocv.IMRead(path, gocv.IMReadColor)
	if img.Empty() {
		return gocv.NewMat(), fmt.Errorf("error reading image file: %s", path)
	}
	return img, nil
}

func SIFT(img gocv.Mat) (gocv.Mat, error) {
	if img.Empty() {
		return gocv.NewMat(), fmt.Errorf("input image is empty")
	}

	sift := gocv.NewSIFT()
	defer sift.Close()

	mask := gocv.NewMat()
	defer mask.Close()
	keypoints, descriptors := sift.DetectAndCompute(img, mask)
	defer descriptors.Close()
	if descriptors.Empty() {
		return gocv.NewMat(), fmt.Errorf("no descriptors found")
	}

	outputImg := img.Clone()
	gocv.DrawKeyPoints(img, keypoints, &outputImg, color.RGBA{0, 255, 0, 0}, gocv.DrawDefault)

	return outputImg, nil
}

func SIFTKeypointsAndDescriptors(img gocv.Mat) ([]gocv.KeyPoint, gocv.Mat, error) {
	if img.Empty() {
		return nil, gocv.NewMat(), fmt.Errorf("input image is empty")
	}

	sift := gocv.NewSIFT()
	defer sift.Close()

	mask := gocv.NewMat()
	defer mask.Close()
	keypoints, descriptors := sift.DetectAndCompute(img, mask)
	if descriptors.Empty() {
		descriptors.Close()
		return nil, gocv.NewMat(), fmt.Errorf("no descriptors found")
	}

	return keypoints, descriptors, nil
}

func SIFTFeatures(img gocv.Mat) ([]float32, error) {
	_, descriptors, err := SIFTKeypointsAndDescriptors(img)
	if err != nil {
		return nil, err
	}
	defer descriptors.Close()

	features := make([]float32, descriptors.Rows()*descriptors.Cols())
	index := 0
	for i := 0; i < descriptors.Rows(); i++ {
		for j := 0; j < descriptors.Cols(); j++ {
			features[index] = descriptors.GetFloatAt(i, j)
			index++
		}
	}

	return features, nil
}
