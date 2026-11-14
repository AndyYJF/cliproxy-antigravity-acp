package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const requestLimit = 24 << 20
const imageLimit = 8 << 20
const imageTotalLimit = 16 << 20

type inputImage struct {
	URL  string
	Data string
	MIME string
}

func imagePart(part object, images *[]inputImage) (object, error) {
	raw, ok := part["image_url"].(string)
	if nested, yes := part["image_url"].(map[string]any); yes {
		raw, ok = nested["url"].(string)
	}
	if !ok || raw == "" {
		return nil, errors.New("Image content requires image_url; file_id is not supported")
	}
	img := inputImage{URL: raw}
	if strings.HasPrefix(raw, "data:") {
		head, data, found := strings.Cut(raw, ",")
		if !found || !strings.HasSuffix(head, ";base64") {
			return nil, errors.New("Images require a base64 data URL or a public HTTPS URL")
		}
		mime := strings.TrimSuffix(strings.TrimPrefix(head, "data:"), ";base64")
		if len(data) > base64.StdEncoding.EncodedLen(imageLimit) {
			return nil, errors.New("Each image must be at most 8 MiB")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(data)
		if err != nil {
			return nil, errors.New("Invalid image base64")
		}
		actual, err := validateImage(decoded)
		if err != nil {
			return nil, err
		}
		if mime != actual {
			return nil, errors.New("Image MIME type does not match its content")
		}
		img = inputImage{Data: base64.StdEncoding.EncodeToString(decoded), MIME: mime}
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
			return nil, errors.New("Remote images require a public HTTPS URL on port 443")
		}
	}
	*images = append(*images, img)
	return object{"type": "input_text", "text": fmt.Sprintf("[Attached image %d]", len(*images))}, nil
}

func normalizeContent(value any, images *[]inputImage, chat bool) (any, error) {
	if text, ok := value.(string); ok {
		return text, nil
	}
	parts, ok := value.([]any)
	if !ok {
		return nil, errors.New("Content must be text or an array of text/image parts")
	}
	output := make([]any, 0, len(parts))
	for _, raw := range parts {
		p, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New("Invalid content part")
		}
		kind, _ := p["type"].(string)
		if (chat && kind == "image_url") || (!chat && kind == "input_image") {
			ref, err := imagePart(p, images)
			if err != nil {
				return nil, err
			}
			output = append(output, ref)
		} else if (chat && kind == "text") || (!chat && (kind == "input_text" || kind == "output_text")) {
			if _, ok := p["text"].(string); !ok {
				return nil, errors.New("Text content must be a string")
			}
			output = append(output, p)
		} else {
			return nil, errors.New("Only text and image input are supported; audio, video and files are unavailable")
		}
	}
	return output, nil
}

func validateImage(data []byte) (string, error) {
	if len(data) == 0 || len(data) > imageLimit {
		return "", errors.New("Each image must be non-empty and at most 8 MiB")
	}
	mime := http.DetectContentType(data)
	if mime == "image/webp" {
		return mime, nil
	}
	if mime != "image/png" && mime != "image/jpeg" && mime != "image/gif" {
		return "", errors.New("Supported images: PNG, JPEG, WebP and GIF")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return "", errors.New("Invalid image or dimensions exceed 40 megapixels")
	}
	return mime, nil
}

func publicImageIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, network := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		_, block, _ := net.ParseCIDR(network)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}

func fetchImage(ctx context.Context, source string) ([]byte, error) {
	transport := &http.Transport{TLSHandshakeTimeout: 8 * time.Second, ResponseHeaderTimeout: 10 * time.Second, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, errors.New("Image host cannot be resolved")
		}
		if len(ips) == 0 {
			return nil, errors.New("Image host cannot be resolved")
		}
		for _, ip := range ips {
			if !publicImageIP(ip.IP) {
				return nil, errors.New("Image URLs must resolve only to public IP addresses")
			}
		}
		dialer := net.Dialer{Timeout: 8 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || req.URL.Scheme != "https" || req.URL.User != nil || (req.URL.Port() != "" && req.URL.Port() != "443") {
			return errors.New("Unsupported image redirect")
		}
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return nil, errors.New("Invalid image URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("Could not fetch image from public HTTPS URL")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, errors.New("Image URL did not return HTTP 200")
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, imageLimit+1))
	if err != nil {
		return nil, errors.New("Could not read image")
	}
	return data, nil
}

func imagePrompt(ctx context.Context, text string, images []inputImage) ([]object, error) {
	blocks := []object{{"type": "text", "text": text}}
	total := 0
	for i, img := range images {
		if img.URL != "" {
			data, err := fetchImage(ctx, img.URL)
			if err != nil {
				return nil, &httpError{400, err.Error()}
			}
			mime, err := validateImage(data)
			if err != nil {
				return nil, &httpError{400, err.Error()}
			}
			img.Data = base64.StdEncoding.EncodeToString(data)
			img.MIME = mime
		}
		total += base64.StdEncoding.DecodedLen(len(img.Data))
		if total > imageTotalLimit {
			return nil, &httpError{400, "Combined images must be at most 16 MiB"}
		}
		blocks = append(blocks, object{"type": "text", "text": fmt.Sprintf("Attached image %d (referenced in the conversation above):", i+1)}, object{"type": "image", "data": img.Data, "mimeType": img.MIME})
	}
	return blocks, nil
}

func (a *acp) checkImages(images []inputImage) error {
	if len(images) > 0 && !a.imageCapable {
		return &httpError{400, "The official ACP agent does not advertise image input support"}
	}
	return nil
}
