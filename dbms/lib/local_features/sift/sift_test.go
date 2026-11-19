package sift

import (
	"image"
	"image/color"
	"testing"

	"gocv.io/x/gocv"
)

func texturedImage(t *testing.T) gocv.Mat {
	t.Helper()

	img := gocv.NewMatWithSize(256, 256, gocv.MatTypeCV8UC1)
	t.Cleanup(func() { img.Close() })

	for y := 16; y < 240; y += 32 {
		for x := 16; x < 240; x += 32 {
			value := uint8(255)
			if (x/32+y/32)%2 == 0 {
				value = 0
			}
			gocv.Rectangle(&img, image.Rect(x, y, x+24, y+24),
				color.RGBA{R: value, G: value, B: value, A: 0}, -1)
		}
	}
	return img
}

func TestSIFTKeypointsAndDescriptors(t *testing.T) {
	img := texturedImage(t)

	keypoints, descriptors, err := SIFTKeypointsAndDescriptors(img)
	if err != nil {
		t.Fatalf("SIFTKeypointsAndDescriptors returned error: %v", err)
	}
	defer descriptors.Close()

	if len(keypoints) == 0 {
		t.Fatal("expected at least one keypoint")
	}
	if descriptors.Empty() {
		t.Fatal("expected non-empty descriptors")
	}
	if descriptors.Rows() != len(keypoints) {
		t.Fatalf("descriptor rows = %d, keypoints = %d", descriptors.Rows(), len(keypoints))
	}
	if descriptors.Cols() != 128 || descriptors.Channels() != 1 {
		t.Fatalf("descriptor shape = %dx%d with %d channels, expected rows x 128 x 1",
			descriptors.Rows(), descriptors.Cols(), descriptors.Channels())
	}
}

func TestSIFTDrawsKeypoints(t *testing.T) {
	img := texturedImage(t)

	output, err := SIFT(img)
	if err != nil {
		t.Fatalf("SIFT returned error: %v", err)
	}
	defer output.Close()

	if output.Empty() {
		t.Fatal("expected a non-empty output image")
	}
	if output.Rows() != img.Rows() || output.Cols() != img.Cols() {
		t.Fatalf("output size = %dx%d, input size = %dx%d",
			output.Rows(), output.Cols(), img.Rows(), img.Cols())
	}
}

func TestSIFTFeaturesFlattensDescriptors(t *testing.T) {
	img := texturedImage(t)

	features, err := SIFTFeatures(img)
	if err != nil {
		t.Fatalf("SIFTFeatures returned error: %v", err)
	}

	keypoints, descriptors, err := SIFTKeypointsAndDescriptors(img)
	if err != nil {
		t.Fatalf("SIFTKeypointsAndDescriptors returned error: %v", err)
	}
	defer descriptors.Close()

	expected := descriptors.Rows() * descriptors.Cols() * descriptors.Channels()
	if len(features) != expected {
		t.Fatalf("feature length = %d, expected %d (%d descriptors x %d columns)",
			len(features), expected, len(keypoints), descriptors.Cols())
	}
	if len(features) != descriptors.Total()*descriptors.Channels() {
		t.Fatalf("feature length = %d, expected total descriptor elements %d",
			len(features), descriptors.Total()*descriptors.Channels())
	}
}

func TestSIFTRejectsImagesWithoutDescriptors(t *testing.T) {
	img := gocv.NewMatWithSize(128, 128, gocv.MatTypeCV8UC1)
	defer img.Close()

	if _, err := SIFT(img); err == nil {
		t.Fatal("SIFT expected an error for a uniform image")
	}
	if _, _, err := SIFTKeypointsAndDescriptors(img); err == nil {
		t.Fatal("SIFTKeypointsAndDescriptors expected an error for a uniform image")
	}
	if _, err := SIFTFeatures(img); err == nil {
		t.Fatal("SIFTFeatures expected an error for a uniform image")
	}
}

func TestSIFTRejectsEmptyImage(t *testing.T) {
	img := gocv.NewMat()
	defer img.Close()

	if _, err := SIFT(img); err == nil {
		t.Fatal("SIFT expected an error for an empty image")
	}
	if _, _, err := SIFTKeypointsAndDescriptors(img); err == nil {
		t.Fatal("SIFTKeypointsAndDescriptors expected an error for an empty image")
	}
	if _, err := SIFTFeatures(img); err == nil {
		t.Fatal("SIFTFeatures expected an error for an empty image")
	}
}
