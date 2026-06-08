package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const maxUploadIconBytes = 2 << 20

var uploadIconTypes = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"image/x-icon":  ".ico",
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
		return
	}

	cfg := s.currentConfig()
	if cfg.Assets.UploadsDir == "" {
		writeJSONError(w, http.StatusBadRequest, "未配置上传目录")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadIconBytes+1024)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "请选择图片文件")
		return
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, maxUploadIconBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "读取图片失败")
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "图片不能为空")
		return
	}
	if len(body) > maxUploadIconBytes {
		writeJSONError(w, http.StatusBadRequest, "图片不能超过 2MB")
		return
	}

	ext, err := uploadIconExt(header.Filename, body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now()
	name, err := randomUploadName(ext)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "生成文件名失败")
		return
	}
	relDir := filepath.Join(fmt.Sprintf("%04d", now.Year()), fmt.Sprintf("%d", int(now.Month())), fmt.Sprintf("%d", now.Day()))
	dir := filepath.Join(cfg.Assets.UploadsDir, relDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		writeJSONError(w, http.StatusBadRequest, "创建上传目录失败")
		return
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, body, 0644); err != nil {
		writeJSONError(w, http.StatusBadRequest, "保存图片失败")
		return
	}

	publicPath := cfg.Assets.UploadsURLPrefix + strings.ReplaceAll(filepath.ToSlash(filepath.Join(relDir, name)), "//", "/")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"url": publicPath})
}

func uploadIconExt(filename string, body []byte) (string, error) {
	contentType := http.DetectContentType(body)
	if ext, ok := uploadIconTypes[contentType]; ok {
		return ext, nil
	}
	fileExt := strings.ToLower(filepath.Ext(filename))
	if fileExt == ".svg" && looksLikeSVG(body) {
		return ".svg", nil
	}
	return "", fmt.Errorf("仅支持 PNG、JPG、WEBP、SVG、ICO 图标")
}

func looksLikeSVG(body []byte) bool {
	text := strings.TrimSpace(strings.ToLower(string(body)))
	return strings.HasPrefix(text, "<svg") || strings.Contains(text, "<svg ")
}

func randomUploadName(ext string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]) + ext, nil
}
