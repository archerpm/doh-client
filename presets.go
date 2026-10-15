package main

// Preset is a group of rules the user can add with one click.
type Preset struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Domains   []string `json:"domains"`
	Providers []string `json:"providers"`
	Note      string   `json:"note"`
}

// Provider chains are ordered by how broad each provider's list is for that category
// (verified 2026-10-13). Subdomains are matched automatically.
var presets = []Preset{
	{ID: "ai", Name: "AI-сервисы",
		Domains: []string{
			"chatgpt.com", "openai.com", "oaistatic.com", "oaiusercontent.com", "sora.com",
			"claude.ai", "claude.com", "anthropic.com",
			"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com", "notebooklm.google.com", "labs.google", "jules.google.com",
			"grok.com", "x.ai",
			"copilot.microsoft.com", "githubcopilot.com",
			"deepl.com", "elevenlabs.io", "perplexity.ai",
		},
		Providers: []string{"malw", "comss", "geohide", "malw-cf", "dnsai"},
		Note:      "ChatGPT, Claude, Gemini, Grok, Copilot, DeepL, ElevenLabs"},
	{ID: "work", Name: "Работа и разработка",
		Domains:   []string{"notion.so", "notion.com", "canva.com", "linear.app", "clickup.com", "autodesk.com", "plugins.jetbrains.com", "datalore.jetbrains.com"},
		Providers: []string{"geohide", "malw", "malw-cf", "comss"},
		Note:      "Notion, Canva, Linear, ClickUp, Autodesk, JetBrains"},
	{ID: "media", Name: "Музыка и видео",
		Domains:   []string{"spotify.com", "scdn.co", "spotifycdn.com", "soundcloud.com", "deezer.com", "tidal.com", "last.fm", "patreon.com", "twitch.tv"},
		Providers: []string{"malw", "malw-cf"},
		Note:      "Spotify, SoundCloud, Deezer, Tidal, Patreon, Twitch"},
	{ID: "games", Name: "Игры",
		Domains:   []string{"supercell.com", "brawlstars.com", "clashofclans.com", "clashroyale.com", "geforcenow.com", "parsec.app"},
		Providers: []string{"malw", "comss", "malw-cf"},
		Note:      "Supercell, GeForce NOW, Parsec"},
}
