# Stub HMM classifier for the smoke stack.
FROM golang:1.26.10-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ ./internal/
COPY smoke/hmmstub/ ./smoke/hmmstub/
RUN CGO_ENABLED=0 go build -o /hmm-stub ./smoke/hmmstub/cmd/hmm-stub

FROM gcr.io/distroless/static-debian12
COPY --from=build /hmm-stub /hmm-stub
ENTRYPOINT ["/hmm-stub"]
