package web

import (
	"os"
	"path/filepath"
	"strings"
)

// SkillMeta 保存从 SKILL.md frontmatter 解析的元数据
type SkillMeta struct {
	Name                 string
	Description          string
	WhenToUse            string
	AllowedTools         []string
	Model                string
	UserInvocable        bool
	DisableModelInvoke   bool
	ArgumentHint         string
	Version              string
	IsPromptSkill        bool // 纯提示词 skill (无 .py)
	HasScript            bool // 有 .py 脚本
	BaseDir              string
	MarkdownContent      string // frontmatter 之后的正文
}

// parseFrontmatter 解析 SKILL.md 的 YAML-like frontmatter
// 格式:
//   ---
//   key: value
//   key2: "quoted value"
//   ---
//   # 正文
func parseFrontmatter(content string) (map[string]string, string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return nil, content
	}
	fm := map[string]string{}
	i := 1
	for i < len(lines) {
		line := lines[i]
		if strings.TrimSpace(line) == "---" {
			i++
			break
		}
		// 解析 key: value
		idx := strings.Index(line, ":")
		if idx > 0 {
			key := strings.TrimSpace(line[:idx])
			val := strings.TrimSpace(line[idx+1:])
			// 去引号
			if len(val) >= 2 {
				if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
					val = val[1 : len(val)-1]
				}
			}
			fm[strings.ToLower(key)] = val
		}
		i++
	}
	body := strings.Join(lines[i:], "\n")
	return fm, body
}

// loadSkillMeta 从 skill 目录加载 SKILL.md 并解析元数据
func loadSkillMeta(skillDir, skillName string) *SkillMeta {
	skillMdPath := filepath.Join(skillDir, skillName, "SKILL.md")
	data, err := os.ReadFile(skillMdPath)
	if err != nil {
		return nil
	}
	fm, body := parseFrontmatter(string(data))
	meta := &SkillMeta{
		Name:            skillName,
		MarkdownContent: body,
		BaseDir:         filepath.Join(skillDir, skillName),
		UserInvocable:   true, // 默认可调用
	}
	if fm != nil {
		meta.Description = fm["description"]
		meta.WhenToUse = fm["when_to_use"]
		meta.Model = fm["model"]
		meta.ArgumentHint = fm["argument-hint"]
		meta.Version = fm["version"]
		if v, ok := fm["user-invocable"]; ok {
			meta.UserInvocable = v == "true" || v == "yes"
		}
		if v, ok := fm["disable-model-invocation"]; ok {
			meta.DisableModelInvoke = v == "true" || v == "yes"
		}
		if v, ok := fm["allowed-tools"]; ok && v != "" {
			for _, t := range strings.Fields(v) {
				meta.AllowedTools = append(meta.AllowedTools, t)
			}
		}
	}
	// description fallback: 取正文第一段非空非标题行
	if meta.Description == "" {
		meta.Description = extractSkillDescription(string(data))
	}
	return meta
}

// Skill 预算常量 (参考 cc-haha)
const (
	skillBudgetContextPercent = 0.01 // 1% 上下文窗口
	skillCharsPerToken        = 4
	skillDefaultCharBudget    = 8000 // 默认 1% * 200k * 4
	skillMaxListingDescChars  = 250  // 单条描述上限
	skillMinDescLength        = 20   // 最小描述长度 (低于此值降级为仅名称)
)

// formatSkillsWithinBudget 将 skill 列表格式化为符合 token 预算的字符串
// 参考 cc-haha formatCommandsWithinBudget: 先尝试完整描述, 超预算则截断/降级
func formatSkillsWithinBudget(metas []*SkillMeta, contextWindowTokens int) string {
	if len(metas) == 0 {
		return "\n当前无已安装技能。将技能 .py 文件或含 SKILL.md 的子目录放入 skills 目录即可。\n"
	}

	budget := skillDefaultCharBudget
	if contextWindowTokens > 0 {
		budget = int(float64(contextWindowTokens) * skillCharsPerToken * skillBudgetContextPercent)
	}

	// 构建完整条目
	type entry struct {
		meta *SkillMeta
		full string
	}
	entries := make([]entry, 0, len(metas))
	for _, m := range metas {
		entries = append(entries, entry{meta: m, full: formatSkillEntry(m)})
	}

	// 计算总长度
	totalLen := 0
	for i, e := range entries {
		totalLen += len(e.full)
		if i < len(entries)-1 {
			totalLen++ // newline
		}
	}

	if totalLen <= budget {
		var sb strings.Builder
		for i, e := range entries {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(e.full)
		}
		return "\n## 当前可用技能\n" + sb.String() + "\n"
	}

	// 超预算: 截断描述
	// 先计算名称开销
	nameOverhead := 0
	for _, e := range entries {
		nameOverhead += len(e.meta.Name) + 6 // "- **name**: " 大约
	}
	nameOverhead += len(entries) - 1 // newlines
	availableForDescs := budget - nameOverhead
	if availableForDescs <= 0 {
		// 极端情况: 仅名称
		var sb strings.Builder
		sb.WriteString("\n## 当前可用技能\n")
		for i, e := range entries {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("- **" + e.meta.Name + "**")
		}
		sb.WriteString("\n")
		return sb.String()
	}

	maxDescLen := availableForDescs / len(entries)
	if maxDescLen < skillMinDescLength {
		// 降级为仅名称
		var sb strings.Builder
		sb.WriteString("\n## 当前可用技能\n")
		for i, e := range entries {
			if i > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("- **" + e.meta.Name + "**")
		}
		sb.WriteString("\n")
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteString("\n## 当前可用技能\n")
	for i, e := range entries {
		if i > 0 {
			sb.WriteString("\n")
		}
		desc := buildSkillDescription(e.meta)
		if len(desc) > maxDescLen {
			desc = desc[:maxDescLen-1] + "…"
		}
		sb.WriteString("- **" + e.meta.Name + "**: " + desc)
	}
	sb.WriteString("\n")
	return sb.String()
}

// formatSkillEntry 格式化单个 skill 条目 (完整版)
func formatSkillEntry(m *SkillMeta) string {
	desc := buildSkillDescription(m)
	tag := ""
	if m.IsPromptSkill {
		tag = " [提示词]"
	}
	return "- **" + m.Name + "**: " + desc + tag
}

// buildSkillDescription 构建描述文本 (description + when_to_use)
func buildSkillDescription(m *SkillMeta) string {
	desc := m.Description
	if desc == "" {
		if m.IsPromptSkill {
			desc = "提示词技能 (查阅 SKILL.md)"
		} else {
			desc = "可用技能"
		}
	}
	if m.WhenToUse != "" {
		desc = desc + " - " + m.WhenToUse
	}
	// 单条上限
	if len(desc) > skillMaxListingDescChars {
		desc = desc[:skillMaxListingDescChars-1] + "…"
	}
	return desc
}
