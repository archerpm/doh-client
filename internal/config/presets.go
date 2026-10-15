package config

// Preset is a group of rules the user can add with one click.
type Preset struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Domains   []string `json:"domains"`
	Providers []string `json:"providers"`
	Note      string   `json:"note"`
	// Optional presets are offered in the UI but are not part of the rules a
	// fresh configuration starts with.
	Optional bool `json:"optional"`
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
				// added from the user's reference list (2026-10-15): AI-specific hosts
				"aiplatform.googleapis.com", "aitestkitchen.withgoogle.com", "alkalicore-pa.clients6.google.com",
				"alkalimakersuite-pa.clients6.google.com", "webchannel-alkalimakersuite-pa.clients6.google.com",
				"antigravity.google.com", "antigravity-hub-auto-updater-974169037036.us-central1.run.app",
				"assistant-s3-pa.googleapis.com", "businessaicode.googleapis.com", "businessaicode.mtls.googleapis.com",
				"cloudcode-pa.googleapis.com", "daily-cloudcode-pa.googleapis.com",
				"discoveryengine.googleapis.com", "discoveryengine.mtls.googleapis.com",
				"eu-discoveryengine.googleapis.com", "eu-discoveryengine.mtls.googleapis.com",
				"us-discoveryengine.googleapis.com", "us-discoveryengine.mtls.googleapis.com",
				"gemini.app.google", "gemini.gstatic.com", "geminiweb-pa.clients6.google.com", "jnn-pa.googleapis.com",
				"ogads-pa.clients6.google.com", "push.clients6.google.com", "signaler-pa.clients6.google.com", "waa-pa.clients6.google.com",
				"cdn.openaimerge.com", "oaistatsig.com", "claudemcpcontent.com", "grokusercontent.com",
				"copilot.com", "copilot.cloud.microsoft", "copilot-proxy.githubusercontent.com",
				"elevenreader.io",
			},
			Providers: []string{"malw", "comss", "geohide", "malw-cf", "dnsai"},
			Note:      "ChatGPT, Claude, Gemini, Grok, Copilot, DeepL, ElevenLabs"},
		{ID: "ai-support", Name: "AI: вход и вспомогательные домены",
			Domains: []string{
				// Google sign-in, assets and APIs the AI web apps load
				"accounts.google.com", "apis.google.com", "consent.google.com", "ogs.google.com", "play.google.com", "www.google.com",
				"www.googleapis.com", "content.googleapis.com", "content-push.googleapis.com", "oauth2.googleapis.com", "maps.googleapis.com",
				"fonts.googleapis.com", "fonts.gstatic.com", "maps.gstatic.com", "ssl.gstatic.com", "www.gstatic.com",
				"encrypted-tbn0.gstatic.com", "encrypted-tbn1.gstatic.com", "encrypted-tbn2.gstatic.com", "encrypted-tbn3.gstatic.com",
				"lh3.google.com", "lh3.googleusercontent.com", "lh5.googleusercontent.com", "streetviewpixels-pa.googleapis.com", "o.pki.goog",
				// GitHub (Copilot sign-in and assets)
				"github.com", "github.githubassets.com", "avatars.githubusercontent.com", "origin-tracker.githubusercontent.com",
				// Microsoft / Bing sign-in and experiments
				"login.live.com", "login.microsoftonline.com", "default.exp-tas.com", "edgeservices.bing.com", "sydney.bing.com",
				// third-party auth and challenge services of the AI sites
				"cdn.auth0.com", "cdn.workos.com", "forwarder.workos.com", "frontend-apps-multi-region.workos.com",
				"images.workoscdn.com", "setup.workos.com", "workos.imgix.net", "challenges.cloudflare.com",
			},
			Providers: []string{"malw", "comss", "geohide", "malw-cf", "dnsai"},
			Note:      "Вход через Google/GitHub/Microsoft, шрифты и CDN, нужные AI-сервисам. Домены общие и не заблокированы: обходу они не нужны, а правило отключает IPv6 (AAAA) для всех сайтов на этих доменах. Поэтому набор не включён по умолчанию",
			Optional:  true},
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
				"geforcenow.com", "gfn.am", "parsec.app",
			},
			Providers: []string{"malw", "comss", "malw-cf"},
			Note:      "Supercell, GeForce NOW, Parsec"},
	}
}

// DefaultRules turns every domain of the non-optional presets into an enabled
// rule (once each).
func DefaultRules() []Rule {
	seen := map[string]bool{}
	rules := []Rule{}
	for _, p := range Presets() {
		if p.Optional {
			continue
		}
		for _, d := range p.Domains {
			if !seen[d] {
				seen[d] = true
				rules = append(rules, Rule{Domain: d, Providers: []string{}, Enabled: true})
			}
		}
	}
	return rules
}
