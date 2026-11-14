package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"net"
	"net/http/httptest"
	"strings"
	"testing"
)

func fixtureImage(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b.Bytes())
}
func TestNativeImagesAcrossChatAndToolHistory(t *testing.T) {
	fixture(t)
	img := fixtureImage(t)
	requests := []struct {
		body     object
		response bool
	}{
		{object{"model": "antigravity-acp/gemini-3.8-flash", "messages": []object{{"role": "user", "content": []object{{"type": "text", "text": "__CHECK_IMAGES__"}, {"type": "image_url", "image_url": object{"url": img}}, {"type": "image_url", "image_url": object{"url": img}}}}}}, false},
		{object{"model": "antigravity-acp/gemini-3.8-flash", "input": []object{{"role": "user", "content": []object{{"type": "input_text", "text": "__CHECK_IMAGES__"}, {"type": "input_image", "image_url": img}}}, {"type": "function_call_output", "call_id": "call1", "output": []object{{"type": "input_image", "image_url": img}}}}}, true},
	}
	for _, test := range requests {
		raw, _ := json.Marshal(test.body)
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/", bytes.NewReader(raw))
		if test.response {
			responses(w, r)
		} else {
			chat(w, r)
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), "LOCAL_FIXTURE_OK") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
func TestImageValidationAndCapability(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "http://example.com/x.png", "https://user:pass@example.com/x", "data:image/png;base64,invalid", "data:image/jpeg;base64," + strings.TrimPrefix(fixtureImage(t), "data:image/png;base64,")} {
		var images []inputImage
		if _, err := imagePart(object{"image_url": u}, &images); err == nil {
			t.Fatal("accepted invalid image")
		}
	}
	var images []inputImage
	ref, err := imagePart(object{"image_url": fixtureImage(t)}, &images)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ref["text"].(string), "base64") {
		t.Fatal("image embedded as text")
	}
	if (&acp{}).checkImages(images) == nil {
		t.Fatal("missing capability accepted")
	}
	if _, err = imagePrompt(context.Background(), "x", []inputImage{{URL: "https://127.0.0.1/x.png"}}); err == nil {
		t.Fatal("private fetch allowed")
	}
	for _, ip := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.100.100.200", "::1", "::ffff:127.0.0.1", "fe80::1", "fc00::1"} {
		if publicImageIP(net.ParseIP(ip)) {
			t.Fatal("private IP accepted", ip)
		}
	}
}

func TestMoreThanEightNativeImages(t *testing.T) {
	fixture(t)
	defer liveSessions.closeAll()
	img := fixtureImage(t)
	for _, responsesAPI := range []bool{false, true} {
		for _, reuse := range []bool{false, true} {
			imageType, textType, path, key := "image_url", "text", "/v1/chat/completions", "messages"
			if responsesAPI {
				imageType, textType, path, key = "input_image", "input_text", "/v1/responses", "input"
			}
			first := []object{{"type": textType, "text": "earlier images"}}
			last := []object{{"type": textType, "text": "__CHECK_IMAGES_12__"}}
			for i := 0; i < 6; i++ {
				first = append(first, object{"type": imageType, "image_url": img})
				last = append(last, object{"type": imageType, "image_url": img})
			}
			body := object{"model": "antigravity-acp/gemini-4-hydrogen", key: []object{{"role": "user", "content": first}, {"role": "assistant", "content": "previous reply"}, {"role": "user", "content": last}}}
			raw, _ := json.Marshal(body)
			r := httptest.NewRequest("POST", path, bytes.NewReader(raw))
			if reuse {
				r.Header.Set("X-ACP-Caller-Scope", "image-count-fixture")
			}
			w := httptest.NewRecorder()
			if responsesAPI {
				responses(w, r)
			} else {
				chat(w, r)
			}
			if w.Code != 200 || !strings.Contains(w.Body.String(), "LOCAL_FIXTURE_OK") {
				t.Fatal(responsesAPI, reuse, w.Code, w.Body.String())
			}
		}
	}
}

func TestImageSizeLimitsRemain(t *testing.T) {
	var images []inputImage
	oversized := "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(imageLimit)+4)
	if _, err := imagePart(object{"image_url": oversized}, &images); err == nil || !strings.Contains(err.Error(), "8 MiB") {
		t.Fatal("per-image limit lost", err)
	}
	data := base64.StdEncoding.EncodeToString(make([]byte, 6<<20))
	if _, err := imagePrompt(context.Background(), "", []inputImage{{Data: data}, {Data: data}, {Data: data}}); err == nil || !strings.Contains(err.Error(), "16 MiB") {
		t.Fatal("combined limit lost", err)
	}
	raw := strings.Repeat(" ", requestLimit+1)
	for _, responseAPI := range []bool{false, true} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(raw))
		w := httptest.NewRecorder()
		if responseAPI {
			responses(w, r)
		} else {
			chat(w, r)
		}
		if w.Code != 413 {
			t.Fatal("HTTP body limit lost", responseAPI, w.Code)
		}
	}
}
