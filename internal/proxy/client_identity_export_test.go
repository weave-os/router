package proxy

import "weave-os/router/internal/requestcontext"

type ClientIdentity = requestcontext.ClientIdentity

type ClientIdentityContextKey = requestcontext.ClientIdentityContextKey

type ClaudeCodeMetadata = requestcontext.ClaudeCodeMetadata

const MaxClientIdentifierLen = requestcontext.MaxClientIdentifierLen

var (
	ParseClaudeCodeMetadata   = requestcontext.ParseClaudeCodeMetadata
	NormalizeClientIdentifier = requestcontext.NormalizeClientIdentifier
)
