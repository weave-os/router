package proxy

// MaxRequestBodyBytes caps inbound request bodies across the Anthropic,
// OpenAI, and Gemini API surfaces. One shared constant so the cap can't
// drift between handler packages that each read it independently.
//
// Matches Anthropic's own 32 MB limit; a lower cap causes Claude Code to show
// users a misleading "max 32MB" error message.
const MaxRequestBodyBytes = 32 * 1024 * 1024
