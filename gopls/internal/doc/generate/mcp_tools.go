// Copyright 2025 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"regexp"
	"strings"
)

// mcpToolInfo holds information about an MCP tool.
type mcpToolInfo struct {
	name        string
	description string
	enabled     bool
}

// extractMCPTools extracts tool definitions from mcp.go source.
func extractMCPTools(src string) ([]mcpToolInfo, error) {
	// Map to store all tools with their descriptions and enabled status
	toolMap := make(map[string]mcpToolInfo)

	// Extract from defaultTools list
	defaultToolsRe := regexp.MustCompile(`defaultTools\s*:=\s*\[\]string\{([^}]+)\}`)
	match := defaultToolsRe.FindStringSubmatch(src)
	if match != nil {
		toolRe := regexp.MustCompile(`"([^"]+)"`)
		for _, m := range toolRe.FindAllStringSubmatch(match[1], -1) {
			toolName := m[1]
			toolMap[toolName] = mcpToolInfo{
				name:    toolName,
				enabled: true,
			}
		}
	}

	// Extract descriptions from case statements in addToolByName
	// Use a more lenient regex that handles multi-line descriptions
	caseRe := regexp.MustCompile(`case "([^"]+)":\s*mcp\.AddTool\([^{]*\{\s*Name:\s*"[^"]*",\s*Description:\s*` + "`" + `([^` + "`" + `]*)`)
	for _, match := range caseRe.FindAllStringSubmatch(src, -1) {
		toolName := match[1]
		description := match[2]

		info := mcpToolInfo{
			name:        toolName,
			description: description,
		}

		// Check if this tool is in the enabled list
		if existing, exists := toolMap[toolName]; exists {
			info.enabled = existing.enabled
		} else {
			info.enabled = false
		}

		toolMap[toolName] = info
	}

	// Convert map to slice, preserving the order from defaultTools
	var tools []mcpToolInfo
	for _, info := range toolMap {
		// Add enabled tools in their original order
		if info.enabled {
			tools = append(tools, info)
		}
	}
	// Then add disabled tools
	for _, info := range toolMap {
		if !info.enabled {
			tools = append(tools, info)
		}
	}

	return tools, nil
}

// rewriteMCPTools updates the mcp.md file with current tool definitions.
func rewriteMCPTools(old []byte, mcpSrc string) ([]byte, error) {
	tools, err := extractMCPTools(mcpSrc)
	if err != nil {
		return nil, err
	}

	// Separate enabled and disabled tools
	var enabledTools, disabledTools []mcpToolInfo
	for _, tool := range tools {
		if tool.enabled {
			enabledTools = append(enabledTools, tool)
		} else {
			disabledTools = append(disabledTools, tool)
		}
	}

	// Generate documentation
	var content strings.Builder
	content.WriteString("### Enabled by default\n\n")
	for _, tool := range enabledTools {
		fmt.Fprintf(&content, "- **%s** — %s\n", tool.name, strings.TrimSpace(tool.description))
	}

	content.WriteString("\n### Disabled by default (can be enabled in configuration)\n\n")
	for _, tool := range disabledTools {
		fmt.Fprintf(&content, "- **%s** — %s\n", tool.name, strings.TrimSpace(tool.description))
	}

	// Replace content between markers
	beginMarker := "<!-- BEGIN_MCP_TOOLS -->"
	endMarker := "<!-- END_MCP_TOOLS -->"

	oldStr := string(old)
	beginIdx := strings.Index(oldStr, beginMarker)
	endIdx := strings.Index(oldStr, endMarker)

	if beginIdx == -1 || endIdx == -1 {
		return nil, fmt.Errorf("mcp.md missing MCP tools markers")
	}

	new := make([]byte, 0, len(old)+len(content.String()))
	new = append(new, old[:beginIdx+len(beginMarker)]...)
	new = append(new, '\n')
	new = append(new, []byte(content.String())...)
	new = append(new, '\n')
	new = append(new, old[endIdx:]...)

	return new, nil
}
