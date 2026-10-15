package config

// Preset is a group of rules the user can add with one click.
type Preset struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Domains   []string `json:"domains"`
	Providers []string `json:"providers"`
	Note      string   `json:"note"`
}

// Presets are the services the client targets. Subdomains are matched
// automatically, so each entry is a registrable or otherwise distinct domain.
// Sources (checked 2026-10-15): the services' own network docs where they exist
// (Anthropic), the public itdoginfo/allow-domains geoblock and google_ai lists
// and CDN hostnames observed for the service; only domains confirmed by one of
// them are listed.
func Presets() []Preset {
	return []Preset{
		{ID: "ai", Name: "AI-сервисы",
			Domains: []string{
				// OpenAI: ChatGPT, Sora
				"chatgpt.com", "openai.com", "oaistatic.com", "oaiusercontent.com", "sora.com",
				"production-openaicom-storage.azureedge.net",
				// Anthropic: Claude (artifacts are served from claudeusercontent.com)
				"claude.ai", "claude.com", "anthropic.com", "claudeusercontent.com",
				// Google AI: Gemini, AI Studio, NotebookLM, Labs, Jules, Stitch, DeepMind, Antigravity
				"gemini.google.com", "gemini.google", "aistudio.google.com", "ai.google.dev", "bard.google.com", "makersuite.google.com",
				"generativelanguage.googleapis.com", "generativeai.google", "notebooklm.google.com", "notebooklm.google",
				"labs.google", "jules.google.com", "jules.google", "stitch.withgoogle.com", "deepmind.google", "deepmind.com",
				"antigravity.google", "antigravity.googleapis.com", "antigravity-pa.googleapis.com", "antigravity-unleash.goog",
				"aida.googleapis.com", "aisandbox-pa.googleapis.com", "geller-pa.googleapis.com", "proactivebackend-pa.googleapis.com",
				"robinfrontend-pa.googleapis.com", "speechs3proto2-pa.googleapis.com",
				// xAI: Grok
				"grok.com", "x.ai", "grok.x.com",
				// Microsoft / GitHub Copilot
				"copilot.microsoft.com", "designer.microsoft.com", "githubcopilot.com",
				"deepl.com", "elevenlabs.io", "perplexity.ai",
			},
			Providers: []string{"malw", "comss", "geohide", "malw-cf", "dnsai"},
			Note:      "ChatGPT, Claude, Gemini, Grok, Copilot, DeepL, ElevenLabs"},
		{ID: "work", Name: "Работа и разработка",
			Domains: []string{
				"notion.so", "notion.com", "notion.site", "notion.new", "notion-static.com",
				"canva.com", "canva.dev", "linear.app", "clickup.com", "autodesk.com",
				"jetbrains.com", "jetbrains.space", "jetbrains.team", "myjetbrains.com", "youtrack.cloud", "qodana.cloud", "api.jetbrains.ai",
			},
			Providers: []string{"geohide", "malw", "malw-cf", "comss"},
			Note:      "Notion, Canva, Linear, ClickUp, Autodesk, JetBrains"},
		{ID: "media", Name: "Музыка и видео",
			Domains: []string{
				"spotify.com", "scdn.co", "spotifycdn.com",
				"soundcloud.com", "sndcdn.com",
				"deezer.com", "dzcdn.net",
				"tidal.com", "last.fm",
				"patreon.com", "patreonusercontent.com",
				"twitch.tv", "ttvnw.net", "jtvnw.net", "twitchcdn.net", "twitchsvc.net",
			},
			Providers: []string{"malw", "malw-cf"},
			Note:      "Spotify, SoundCloud, Deezer, Tidal, Patreon, Twitch"},
		{ID: "games", Name: "Игры",
			Domains: []string{
				"supercell.com", "brawlstars.com", "brawlstarsgame.com", "clashofclans.com", "clashroyale.com", "clashroyaleapp.com",
				"haydaygame.com", "squadbustersgame.com",
				"geforcenow.com", "gfn.am", "nvidiagrid.net", "parsec.app",
			},
			Providers: []string{"malw", "comss", "malw-cf"},
			Note:      "Supercell, GeForce NOW, Parsec"},
	}
}

// DefaultRules turns every preset domain into an enabled rule (once each).
func DefaultRules() []Rule {
	seen := map[string]bool{}
	rules := []Rule{}
	for _, p := range Presets() {
		for _, d := range p.Domains {
			if !seen[d] {
				seen[d] = true
				rules = append(rules, Rule{Domain: d, Providers: []string{}, Enabled: true})
			}
		}
	}
	return rules
}
