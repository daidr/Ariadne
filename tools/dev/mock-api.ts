/** A local OpenAI-compatible service for exercising the real provider and agent loop. */
export const MOCK_MODELS = ['mock', 'mock-slow', 'mock-error'] as const

interface WireMessage {
  role: string
  content?: unknown
}

export function startMockApi() {
  return Bun.serve({
    hostname: '127.0.0.1',
    port: 0,
    async fetch(request) {
      const path = new URL(request.url).pathname
      if (request.method === 'GET' && path === '/v1/models') {
        return Response.json({
          object: 'list',
          data: MOCK_MODELS.map((id) => ({ id, object: 'model', owned_by: 'ariadne-dev' })),
        })
      }
      if (request.method === 'POST' && path === '/v1/search') {
        if (request.headers.get('authorization') !== 'Bearer dev-only') {
          return Response.json({ error: { message: 'Invalid development key' } }, { status: 401 })
        }
        const { query } = (await request.json()) as { query: string }
        return Response.json({
          results: [
            {
              title: `模拟搜索：${query}`,
              url: 'https://example.invalid/dev-search',
              content: '来自本机开发模拟接口',
              snippet: '来自本机开发模拟接口',
              summary: '来自本机开发模拟接口',
            },
          ],
        })
      }
      if (request.method !== 'POST' || path !== '/v1/chat/completions') {
        return new Response('Not found', { status: 404 })
      }
      const body = (await request.json()) as {
        model: string
        messages: WireMessage[]
        tools?: { function: { name: string } }[]
        stream?: boolean
      }
      if (!MOCK_MODELS.includes(body.model as (typeof MOCK_MODELS)[number])) {
        return Response.json({ error: { message: 'Unknown mock model' } }, { status: 404 })
      }
      if (body.model === 'mock-error') {
        return Response.json(
          { error: { message: '开发模拟：模型请求失败，请切回 mock 后重试。' } },
          { status: 400 },
        )
      }
      const user = body.messages.findLast((message) => message.role === 'user')
      const input =
        typeof user?.content === 'string' ? user.content : JSON.stringify(user?.content ?? '')
      // Tool results terminate the step: never repeatedly send the same message.
      const answered = body.messages.at(-1)?.role === 'tool'
      const text = answered ? '' : `模拟 AI 已收到消息。\n\n${input.slice(-600)}`
      const tool = !answered && body.tools?.some((item) => item.function.name === 'send_message')
      const call = {
        id: `call_${crypto.randomUUID()}`,
        type: 'function',
        function: { name: 'send_message', arguments: JSON.stringify({ text }) },
      }
      const finish = tool ? 'tool_calls' : 'stop'
      const usage = { prompt_tokens: 100, completion_tokens: 30, total_tokens: 130 }
      if (!body.stream) {
        return Response.json({
          id: call.id,
          object: 'chat.completion',
          model: body.model,
          choices: [
            {
              index: 0,
              message: {
                role: 'assistant',
                content: tool ? null : text,
                ...(tool ? { tool_calls: [call] } : {}),
              },
              finish_reason: finish,
            },
          ],
          usage,
        })
      }
      const encoder = new TextEncoder()
      let cancelled = false
      const stream = new ReadableStream<Uint8Array>({
        async start(controller) {
          const chunk = (delta: unknown, finishReason: string | null = null) => {
            controller.enqueue(
              encoder.encode(
                `data: ${JSON.stringify({
                  id: call.id,
                  object: 'chat.completion.chunk',
                  model: body.model,
                  choices: [{ index: 0, delta, finish_reason: finishReason }],
                  ...(finishReason ? { usage } : {}),
                })}\n\n`,
              ),
            )
          }
          try {
            chunk({
              role: 'assistant',
              ...(tool
                ? {
                    tool_calls: [
                      { index: 0, ...call, function: { name: 'send_message', arguments: '' } },
                    ],
                  }
                : {}),
            })
            const output = tool ? call.function.arguments : text
            for (let i = 0; i < output.length; i += 16) {
              if (cancelled) break
              await Bun.sleep(body.model === 'mock-slow' ? 180 : 25)
              if (cancelled) break
              const part = output.slice(i, i + 16)
              chunk(
                tool
                  ? { tool_calls: [{ index: 0, function: { arguments: part } }] }
                  : { content: part },
              )
            }
            if (!cancelled) {
              chunk({}, finish)
              controller.enqueue(encoder.encode('data: [DONE]\n\n'))
              controller.close()
            }
          } catch (error) {
            if (!cancelled) controller.error(error)
          }
        },
        cancel() {
          cancelled = true
        },
      })
      return new Response(stream, { headers: { 'content-type': 'text/event-stream' } })
    },
  })
}
