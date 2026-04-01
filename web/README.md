# mmmanus Web Frontend

Vue 3 + Vite + TypeScript frontend for using the assistant through the existing OpenAI-compatible backend.

## Features (MVP)

- Streaming chat rendering from `/v1/chat/completions` (NDJSON)
- Multi-agent model switching (`host`, `deepresearch`, `urlreader`, `lbshelper`)
- Conversation history (local persistence)
- Task state badges (`queued`, `running`, `completed`, `failed`, `canceled`)
- Request cancel using `AbortController`
- File upload staging UX (metadata forwarded in the prompt)

## Run

1. Start backend in repository root:

```bash
go run . allinone -c ./trpc_go.yaml -m config.yaml
```

2. Start frontend:

```bash
cd web
npm install
npm run dev
```

3. Open:

`http://127.0.0.1:5173`

## Environment

Copy `.env.example` to `.env` and edit if needed:

```env
VITE_API_BASE_URL=http://127.0.0.1:11000
VITE_DEV_PROXY_TARGET=http://127.0.0.1:11000
```

## Notes

- This MVP assumes internal usage and no auth.
- Upload currently works as staged metadata in chat input; binary processing API can be added next.
