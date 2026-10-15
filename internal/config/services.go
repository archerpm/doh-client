package config

// Service is a named service with the hosts whose DNS answers show whether a
// provider proxies it. A host is a real endpoint of the service (subdomains
// matter: the apex of a CDN-fronted service often has no address at all).
type Service struct {
	Name     string   `json:"name"`
	Category string   `json:"category"`
	Hosts    []string `json:"hosts"`
}

// Service categories.
const (
	CatAI    = "AI"
	CatAIDev = "AI-разработка"
	CatWork  = "Работа и разработка"
	CatMedia = "Музыка и видео"
	CatSocal = "Соцсети и общение"
	CatGames = "Игры"
)

// ServiceProbes is the catalogue used to describe what a provider supports.
// A provider supports a service when at least half of its hosts resolve to the
// provider's proxy addresses (see tools/probesupport).
func ServiceProbes() []Service {
	return []Service{
		// AI
		{"ChatGPT", CatAI, []string{"chatgpt.com", "api.openai.com", "auth.openai.com"}},
		{"Sora", CatAI, []string{"sora.com"}},
		{"Claude", CatAI, []string{"claude.ai", "api.anthropic.com", "console.anthropic.com"}},
		{"Gemini", CatAI, []string{"gemini.google.com", "aistudio.google.com", "generativelanguage.googleapis.com"}},
		{"NotebookLM", CatAI, []string{"notebooklm.google.com", "notebook.google.com"}},
		{"Google Labs, Flow, Opal", CatAI, []string{"labs.google", "flow.google", "opal.google"}},
		{"Grok", CatAI, []string{"grok.com", "accounts.x.ai", "x.ai"}},
		{"Microsoft Copilot", CatAI, []string{"copilot.microsoft.com", "copilot.com"}},
		{"GitHub Copilot", CatAI, []string{"githubcopilot.com", "api.githubcopilot.com"}},
		{"DeepL", CatAI, []string{"deepl.com", "www.deepl.com"}},
		{"ElevenLabs", CatAI, []string{"elevenlabs.io", "api.elevenlabs.io"}},
		{"Perplexity", CatAI, []string{"perplexity.ai", "www.perplexity.ai"}},
		{"Meta AI", CatAI, []string{"meta.ai"}},
		{"Manus", CatAI, []string{"manus.im"}},
		{"Monica", CatAI, []string{"monica.im"}},
		{"Pi", CatAI, []string{"pi.ai"}},
		{"Hume", CatAI, []string{"hume.ai"}},
		{"Genspark", CatAI, []string{"genspark.ai"}},
		{"Reve", CatAI, []string{"reve.com", "reve.art"}},
		// AI development
		{"Cursor", CatAIDev, []string{"cursor.com", "api2.cursor.sh", "authenticate.cursor.sh"}},
		{"Windsurf и Codeium", CatAIDev, []string{"windsurf.com", "codeium.com"}},
		{"Trae", CatAIDev, []string{"trae.ai", "api.trae.ai"}},
		{"OpenCode", CatAIDev, []string{"opencode.ai"}},
		{"Qoder", CatAIDev, []string{"qoder.com"}},
		{"Groq", CatAIDev, []string{"groq.com", "api.groq.com", "console.groq.com"}},
		{"Cerebras", CatAIDev, []string{"cerebras.ai"}},
		{"OpenRouter", CatAIDev, []string{"openrouter.ai"}},
		{"Inception", CatAIDev, []string{"inceptionlabs.ai"}},
		// work and development
		{"Notion", CatWork, []string{"notion.so", "www.notion.so", "notion.com"}},
		{"Canva", CatWork, []string{"canva.com", "www.canva.com"}},
		{"Linear", CatWork, []string{"linear.app", "api.linear.app"}},
		{"ClickUp", CatWork, []string{"clickup.com", "app.clickup.com"}},
		{"Autodesk", CatWork, []string{"autodesk.com", "accounts.autodesk.com"}},
		{"JetBrains", CatWork, []string{"jetbrains.com", "plugins.jetbrains.com", "account.jetbrains.com"}},
		{"Atlassian", CatWork, []string{"atlassian.com", "id.atlassian.com"}},
		{"Slack", CatWork, []string{"slack.com"}},
		{"Zapier", CatWork, []string{"zapier.com"}},
		{"Make", CatWork, []string{"make.com"}},
		{"n8n", CatWork, []string{"n8n.cloud"}},
		{"Adobe", CatWork, []string{"adobe.io", "www.adobe.com"}},
		{"MongoDB", CatWork, []string{"mongodb.com"}},
		{"Basecamp", CatWork, []string{"basecamp.com"}},
		{"Tableau", CatWork, []string{"tableau.com"}},
		{"SketchUp", CatWork, []string{"sketchup.com"}},
		// music and video
		{"Spotify", CatMedia, []string{"spotify.com", "open.spotify.com", "api.spotify.com"}},
		{"SoundCloud", CatMedia, []string{"soundcloud.com", "api-v2.soundcloud.com"}},
		{"Deezer", CatMedia, []string{"deezer.com", "www.deezer.com"}},
		{"Tidal", CatMedia, []string{"tidal.com", "listen.tidal.com"}},
		{"Last.fm", CatMedia, []string{"last.fm", "www.last.fm"}},
		{"Patreon", CatMedia, []string{"patreon.com", "www.patreon.com"}},
		{"Twitch", CatMedia, []string{"twitch.tv", "gql.twitch.tv", "usher.ttvnw.net"}},
		{"Crunchyroll", CatMedia, []string{"crunchyroll.com", "www.crunchyroll.com"}},
		{"Netflix", CatMedia, []string{"netflix.com", "www.netflix.com"}},
		{"Disney+", CatMedia, []string{"disneyplus.com", "www.disneyplus.com"}},
		{"Max", CatMedia, []string{"max.com"}},
		{"YouTube", CatMedia, []string{"youtube.com", "www.youtube.com"}},
		// social
		{"Discord", CatSocal, []string{"discord.com", "gateway.discord.gg"}},
		{"Reddit", CatSocal, []string{"reddit.com", "www.reddit.com"}},
		{"TikTok", CatSocal, []string{"tiktok.com", "www.tiktok.com"}},
		{"Instagram", CatSocal, []string{"instagram.com", "www.instagram.com"}},
		{"Facebook", CatSocal, []string{"facebook.com", "www.facebook.com"}},
		{"X (Twitter)", CatSocal, []string{"x.com", "twitter.com"}},
		// games
		{"Brawl Stars", CatGames, []string{"game.brawlstarsgame.com", "brawlstars.com"}},
		{"Clash Royale", CatGames, []string{"game.clashroyaleapp.com", "clashroyale.com"}},
		{"Clash of Clans", CatGames, []string{"game.clashofclans.com", "game-assets.clashofclans.com"}},
		{"Hay Day", CatGames, []string{"game.haydaygame.com", "hayday.com"}},
		{"Supercell ID", CatGames, []string{"id.supercell.com", "supercell.com"}},
		{"GeForce NOW", CatGames, []string{"geforcenow.com", "play.geforcenow.com"}},
		{"Parsec", CatGames, []string{"parsec.app", "public.parsec.app"}},
		{"Xbox", CatGames, []string{"auth.xboxlive.com", "xsts.auth.xboxlive.com"}},
	}
}
