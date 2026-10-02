package server

import (
	"encoding/json"
	"html/template"
)

// Interface icons are bundled so navigation controls do not depend on an external icon service.
var uiIconShapes = map[string]string{
	"mdi:magnify":                      `<circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4 4"/>`,
	"mdi:plus":                         `<path d="M12 5v14M5 12h14"/>`,
	"mdi:close":                        `<path d="m6 6 12 12M18 6 6 18"/>`,
	"mdi:pencil-box-outline":           `<path d="m15 4 5 5-10 10-6 1 1-6L15 4Zm-8 8 5 5"/>`,
	"mdi:cursor-default-click-outline": `<path d="m15 4 5 5-10 10-6 1 1-6L15 4Zm-8 8 5 5"/>`,
	"mdi:folder-cog-outline":           `<path d="M3 7h7l2 2h9v11H3V7Z"/><circle cx="16" cy="15" r="2"/><path d="M16 11v1m0 6v1m-4-4h1m6 0h1"/>`,
	"mdi:image-multiple-outline":       `<rect x="5" y="3" width="16" height="16" rx="2"/><path d="M2 7v14h14M5 16l5-5 4 4 2-2 5 5"/><circle cx="16" cy="7" r="1"/>`,
	"mdi:image-edit-outline":           `<rect x="3" y="3" width="18" height="18" rx="2"/><path d="m3 16 6-6 5 5 3-3 4 4"/><circle cx="16" cy="7" r="1"/>`,
	"mdi:wallpaper":                    `<rect x="3" y="3" width="18" height="18" rx="2"/><path d="m3 16 6-6 5 5 3-3 4 4"/><circle cx="16" cy="7" r="1"/>`,
	"mdi:image-plus-outline":           `<path d="M14 3H3v18h18V10M17 3v6m-3-3h6M3 16l6-6 5 5 3-3 4 4"/>`,
	"mdi:open-in-new":                  `<path d="M14 3h7v7m-9 2 9-9M10 3H3v18h18v-7"/>`,
	"mdi:link-variant":                 `<path d="m10 14 4-4m-5 6-2 2a4 4 0 0 1-6-6l4-4a4 4 0 0 1 6 0m2 0 2-2a4 4 0 0 1 6 6l-4 4a4 4 0 0 1-6 0"/>`,
	"mdi:content-copy":                 `<rect x="8" y="8" width="12" height="13" rx="2"/><path d="M15 8V3H3v13h5"/>`,
	"mdi:trash-can-outline":            `<path d="M3 6h18M9 6V3h6v3M5 6l1 15h12l1-15M10 10v7m4-7v7"/>`,
	"mdi:upload":                       `<path d="M12 16V3m-5 5 5-5 5 5M3 16v5h18v-5"/>`,
	"mdi:content-save-outline":         `<path d="M3 3h15l3 3v15H3V3Zm4 0v6h9V3M7 21v-7h10v7"/>`,
	"mdi:arrow-up":                     `<path d="M12 20V4m-6 6 6-6 6 6"/>`,
	"mdi:arrow-down":                   `<path d="M12 4v16m-6-6 6 6 6-6"/>`,
	"mdi:chevron-down":                 `<path d="m6 9 6 6 6-6"/>`,
	"mdi:logout":                       `<path d="M10 3H3v18h7m3-5 5-4-5-4m-6 4h12"/>`,
	"mdi:eye":                          `<path d="M2 12s4-7 10-7 10 7 10 7-4 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>`,
	"mdi:eye-off":                      `<path d="m3 3 18 18M10 5h2c6 0 10 7 10 7a23 23 0 0 1-4 4M6 6a22 22 0 0 0-4 6s4 7 10 7h2m-5-7a3 3 0 0 0 3 3"/>`,
	"mdi:web":                          `<circle cx="12" cy="12" r="9"/><ellipse cx="12" cy="12" rx="4" ry="9"/><path d="M3 12h18"/>`,
	"mdi:lan":                          `<rect x="8" y="3" width="8" height="6" rx="1"/><rect x="2" y="16" width="7" height="5" rx="1"/><rect x="15" y="16" width="7" height="5" rx="1"/><path d="M12 9v4M5 16v-3h14v3"/>`,
	"mdi:dots-horizontal":              `<circle cx="5" cy="12" r="1"/><circle cx="12" cy="12" r="1"/><circle cx="19" cy="12" r="1"/>`,
	"mdi:star-outline":                 `<path d="m12 3 3 6 6 1-4.5 4.5 1 6L12 18l-5.5 2.5 1-6L3 10l6-1 3-6Z"/>`,
	"mdi:drag":                         `<path d="M9 5h.01M15 5h.01M9 12h.01M15 12h.01M9 19h.01M15 19h.01" stroke-width="4"/>`,
	"mdi:check":                        `<path d="m4 12 5 5L20 6"/>`,
	"mdi:format-list-bulleted":         `<path d="M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01"/>`,
}

func bundledIcon(name string) template.HTML {
	shape, ok := uiIconShapes[name]
	if !ok {
		return ""
	}
	return template.HTML(`<span class="inline-icon" aria-hidden="true"><svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">` + shape + `</svg></span>`)
}

func iconSetJSON() template.JS {
	icons := make(map[string]string, len(uiIconShapes))
	for name := range uiIconShapes {
		icons[name] = string(bundledIcon(name))
	}
	body, _ := json.Marshal(icons)
	return template.JS(body)
}
