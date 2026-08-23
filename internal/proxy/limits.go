package proxy

// MaxRequestBodyBytes caps inbound request bodies across the Anthropic,
// OpenAI, and Gemini API surfaces. One shared constant so the cap can't
// drift between handler packages that each read it independently.
//
// Matches Anthropic's own 32 MB request limit: a lower cap rejects requests
// the upstream would have served, and Claude Code renders any 413 with its
// canned "max 32MB" copy, so a router-specific cap surfaces to the user as a
// wrong error message.
const MaxRequestBodyBytes = 32 * 1024 * 1024
