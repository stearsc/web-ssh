export class ApiError extends Error {
  readonly code: string
  constructor(code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.code = code
  }
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

export interface HealthView { status: 'ready' }

export async function getHealth(signal: AbortSignal): Promise<HealthView> {
  const response = await fetch('/api/health', {
    credentials: 'same-origin',
    cache: 'no-store',
    signal,
  })
  const body: unknown = await response.json()
  if (!isObject(body) || typeof body.message !== 'string' || !isObject(body.data)) {
    throw new ApiError('INVALID_RESPONSE', '服务返回了无法识别的响应')
  }
  if (!response.ok || body.code !== 0) {
    if (typeof body.code !== 'string') {
      throw new ApiError('INVALID_RESPONSE', '服务返回了无法识别的响应')
    }
    throw new ApiError(body.code, body.message)
  }
  if (body.data.status !== 'ready') {
    throw new ApiError('INVALID_RESPONSE', '服务状态暂不可用')
  }
  return { status: 'ready' }
}
