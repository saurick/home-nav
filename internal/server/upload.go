package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxUploadImageBytes = 8 << 20

var uploadImageTypes = map[string]string{
	"image/gif":     ".gif",
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"image/x-icon":  ".ico",
}

type AssetItem struct {
	URL     string   `json:"url"`
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Size    int64    `json:"size"`
	ModTime string   `json:"mod_time"`
	Width   int      `json:"width,omitempty"`
	Height  int      `json:"height,omitempty"`
	UsedBy  []string `json:"used_by"`
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

	r.Body = http.MaxBytesReader(w, r.Body, maxUploadImageBytes+1024)
	file, header, err := r.FormFile("file")
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "请选择图片文件")
		return
	}
	defer file.Close()

	body, err := io.ReadAll(io.LimitReader(file, maxUploadImageBytes+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "读取图片失败")
		return
	}
	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "图片不能为空")
		return
	}
	if len(body) > maxUploadImageBytes {
		writeJSONError(w, http.StatusBadRequest, "图片不能超过 8MB")
		return
	}

	ext, err := uploadImageExt(header.Filename, body)
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

func (s *Server) handleAssets(w http.ResponseWriter, r *http.Request) {
	if !s.authenticated(r) {
		writeJSONError(w, http.StatusUnauthorized, "请先登录")
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleAssetList(w)
	case http.MethodDelete:
		s.handleAssetDelete(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "请求方法不支持")
	}
}

func (s *Server) handleAssetList(w http.ResponseWriter) {
	cfg := s.currentConfig()
	assets, err := listUploadAssets(cfg)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{"assets": assets})
}

func (s *Server) handleAssetDelete(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	publicURL := r.URL.Query().Get("url")
	if publicURL == "" {
		var payload struct {
			URL string `json:"url"`
		}
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&payload); err != nil {
			writeJSONError(w, http.StatusBadRequest, "请选择要删除的资源")
			return
		}
		publicURL = payload.URL
	}

	usedBy := assetUsage(cfg, publicURL)
	if len(usedBy) > 0 {
		writeJSONError(w, http.StatusConflict, "资源正在使用中，请先从背景或入口图标中移除")
		return
	}

	filePath, relPath, err := uploadFilePath(cfg.Assets, publicURL)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			writeJSONError(w, http.StatusNotFound, "资源不存在")
			return
		}
		writeJSONError(w, http.StatusBadRequest, "读取资源失败")
		return
	}
	if info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "不能删除目录")
		return
	}
	if !supportedUploadExt(filepath.Ext(filePath)) {
		writeJSONError(w, http.StatusBadRequest, "只能删除图片资源")
		return
	}
	if err := os.Remove(filePath); err != nil {
		writeJSONError(w, http.StatusBadRequest, "删除资源失败")
		return
	}
	pruneEmptyUploadDirs(cfg.Assets.UploadsDir, filepath.Dir(filepath.Join(cfg.Assets.UploadsDir, filepath.FromSlash(relPath))))

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{"deleted": publicURL})
}

func uploadImageExt(filename string, body []byte) (string, error) {
	contentType := http.DetectContentType(body)
	if ext, ok := uploadImageTypes[contentType]; ok {
		return ext, nil
	}
	fileExt := strings.ToLower(filepath.Ext(filename))
	if fileExt == ".svg" && looksLikeSVG(body) {
		return ".svg", nil
	}
	if fileExt == ".webp" && strings.HasPrefix(contentType, "application/octet-stream") {
		return ".webp", nil
	}
	return "", fmt.Errorf("仅支持 PNG、JPG、WEBP、GIF、SVG、ICO 图片")
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

func listUploadAssets(cfg *Config) ([]AssetItem, error) {
	if cfg.Assets.UploadsDir == "" {
		return nil, fmt.Errorf("未配置上传目录")
	}

	items := make([]AssetItem, 0)
	err := filepath.WalkDir(cfg.Assets.UploadsDir, func(filePath string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if !supportedUploadExt(filepath.Ext(filePath)) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relPath, err := filepath.Rel(cfg.Assets.UploadsDir, filePath)
		if err != nil {
			return err
		}
		publicURL := cfg.Assets.UploadsURLPrefix + strings.TrimPrefix(filepath.ToSlash(relPath), "/")
		width, height := assetImageSize(filePath)
		usedBy := assetUsage(cfg, publicURL)
		items = append(items, AssetItem{
			URL:     publicURL,
			Name:    filepath.Base(filePath),
			Type:    assetType(filePath, width, height, usedBy),
			Size:    info.Size(),
			ModTime: info.ModTime().Format(time.RFC3339),
			Width:   width,
			Height:  height,
			UsedBy:  usedBy,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取图库失败: %w", err)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ModTime > items[j].ModTime
	})
	return items, nil
}

func assetUsage(cfg *Config, publicURL string) []string {
	var usedBy []string
	if publicURL == "" {
		return usedBy
	}
	if cfg.Appearance.BackgroundImage == publicURL {
		usedBy = append(usedBy, "页面背景")
	}
	for _, group := range cfg.Groups {
		for _, service := range group.Services {
			if service.Icon == publicURL {
				usedBy = append(usedBy, "入口图标: "+service.Name)
			}
		}
	}
	return usedBy
}

func assetType(filePath string, width, height int, usedBy []string) string {
	for _, usage := range usedBy {
		if usage == "页面背景" {
			return "wallpaper"
		}
		if strings.HasPrefix(usage, "入口图标: ") {
			return "icon"
		}
	}
	ext := strings.ToLower(filepath.Ext(filePath))
	if ext == ".svg" || ext == ".ico" {
		return "icon"
	}
	if width > 0 && height > 0 {
		if width >= 800 && width > height {
			return "wallpaper"
		}
		return "icon"
	}
	return "unknown"
}

func assetImageSize(filePath string) (int, int) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, 0
	}
	defer file.Close()

	cfg, _, err := image.DecodeConfig(file)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

func uploadFilePath(assets AssetsConfig, publicURL string) (string, string, error) {
	if assets.UploadsDir == "" {
		return "", "", fmt.Errorf("未配置上传目录")
	}
	publicURL = strings.TrimSpace(publicURL)
	if publicURL == "" {
		return "", "", fmt.Errorf("请选择要操作的资源")
	}
	if !strings.HasPrefix(publicURL, assets.UploadsURLPrefix) {
		return "", "", fmt.Errorf("只能操作上传目录内的资源")
	}
	rel := strings.TrimPrefix(publicURL, assets.UploadsURLPrefix)
	rel = strings.TrimPrefix(rel, "/")
	if rel == "" || strings.Contains(rel, "\x00") {
		return "", "", fmt.Errorf("资源路径无效")
	}
	for _, segment := range strings.Split(rel, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", fmt.Errorf("资源路径无效")
		}
	}
	cleanRel := strings.TrimPrefix(pathpkg.Clean("/"+rel), "/")
	fullPath := filepath.Join(assets.UploadsDir, filepath.FromSlash(cleanRel))
	root, err := filepath.Abs(assets.UploadsDir)
	if err != nil {
		return "", "", fmt.Errorf("上传目录无效")
	}
	full, err := filepath.Abs(fullPath)
	if err != nil {
		return "", "", fmt.Errorf("资源路径无效")
	}
	relToRoot, err := filepath.Rel(root, full)
	if err != nil || relToRoot == "." || strings.HasPrefix(relToRoot, ".."+string(filepath.Separator)) || relToRoot == ".." {
		return "", "", fmt.Errorf("资源路径无效")
	}
	return full, filepath.ToSlash(cleanRel), nil
}

func supportedUploadExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".gif", ".ico", ".jpg", ".jpeg", ".png", ".svg", ".webp":
		return true
	default:
		return false
	}
}

func pruneEmptyUploadDirs(root, dir string) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return
	}
	for {
		dirAbs, err := filepath.Abs(dir)
		if err != nil || dirAbs == rootAbs {
			return
		}
		rel, err := filepath.Rel(rootAbs, dirAbs)
		if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
			return
		}
		if err := os.Remove(dirAbs); err != nil {
			return
		}
		dir = filepath.Dir(dirAbs)
	}
}
