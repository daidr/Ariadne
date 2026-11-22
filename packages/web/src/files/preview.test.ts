import {
  FileSheetRowSchema,
  GetFileSheetRequestSchema,
  GetFileSheetResponseSchema,
  LocalizedErrorSchema,
} from '@ariadne/proto'
import { create, fromBinary, toBinary } from '@bufbuild/protobuf'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { ApiError, fetchSheet, saveToken } from '../api'
import { parseDelimited, previewKind, previewSheetData } from './preview'

describe('file preview', () => {
  test('selects a renderer from stored MIME and the filename', () => {
    expect(previewKind('text/plain; charset=utf-8', 'data.CSV')).toBe('csv')
    expect(previewKind('text/plain', 'config.json')).toBe('text')
    expect(previewKind('video/mp4', '')).toBe('video')
    expect(previewKind('application/zip', 'archive.zip')).toBeUndefined()
    expect(previewKind('application/pdf', 'spec.pdf')).toBeUndefined()
    expect(previewKind('application/octet-stream', 'image.png')).toBeUndefined()
  })

  test('keeps quoted separators, newlines and empty cells as spreadsheet values', () => {
    expect(parseDelimited('a,b,c\r\n"x,y","x""y","x\ny"\r\n"",,\n""', ',')).toEqual({
      rows: [['a', 'b', 'c'], ['x,y', 'x"y', 'x\ny'], ['', '', ''], ['']],
      truncated: false,
    })
    expect(parseDelimited('a\tb\n1\t2', '\t').rows).toEqual([
      ['a', 'b'],
      ['1', '2'],
    ])
  })

  test('bounds large spreadsheets', () => {
    const oversized = parseDelimited('x,'.repeat(45) + 'x\n' + 'x'.repeat(5000), ',')
    expect(oversized.rows[0]).toHaveLength(40)
    expect(oversized.rows[1]?.[0]).toHaveLength(4096)
    expect(oversized.truncated).toBe(true)
    expect(parseDelimited('x\n'.repeat(250), ',').truncated).toBe(false)
    expect(parseDelimited('x\n'.repeat(251), ',').rows).toHaveLength(250)
  })

  test('places worksheet cells at their original row and keeps formula-looking values as text', () => {
    const sheet = previewSheetData('Data', [['001', '=2+2'], ['end'], ['invalid']], [3, 8, 2e9])
    expect(sheet.rowCount).toBe(8)
    expect(sheet.colCount).toBe(2)
    expect(sheet.cells[0]).toBeUndefined()
    expect(sheet.cells[2]).toEqual(['001', "'=2+2"])
    expect(sheet.cells[7]).toEqual(['end'])
  })
})

describe('fetchSheet ConnectRPC client', () => {
  afterEach(() => {
    saveToken('')
    vi.restoreAllMocks()
  })

  test('calls FileService.GetFileSheet over binary ConnectRPC and maps response fields', async () => {
    saveToken('sheet-token')
    const controller = new AbortController()
    let capturedUrl = ''
    let capturedAuth = ''
    let capturedContentType = ''
    let signalPresent = false
    let signalAbortedDuringFetch = true
    let decodedRequest: { fileId: string; index: number } | null = null

    const responseBytes = toBinary(
      GetFileSheetResponseSchema,
      create(GetFileSheetResponseSchema, {
        sheets: ['Checks', 'Next'],
        rows: [
          create(FileSheetRowSchema, { cells: ['', 'Build passed', '', 'true'] }),
          create(FileSheetRowSchema, { cells: ['Done'] }),
        ],
        rowNumbers: [3n, 8n],
        truncated: true,
      }),
    )

    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      capturedUrl =
        typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
      const headers = new Headers(init?.headers)
      capturedAuth = headers.get('authorization') ?? ''
      capturedContentType = headers.get('content-type') ?? ''
      signalPresent = init?.signal instanceof AbortSignal
      signalAbortedDuringFetch = init?.signal?.aborted ?? true
      const bodyBytes =
        init?.body instanceof Uint8Array
          ? init.body
          : new Uint8Array(await new Response(init?.body).arrayBuffer())
      const parsed = fromBinary(GetFileSheetRequestSchema, bodyBytes)
      decodedRequest = { fileId: parsed.fileId, index: parsed.index }
      return new Response(new Uint8Array(responseBytes), {
        status: 200,
        headers: {
          'content-type': 'application/proto',
        },
      })
    })

    const result = await fetchSheet('fil_sheet1', 1, controller.signal)

    expect(capturedUrl).toBe('/api/rpc/ariadne.v1.FileService/GetFileSheet')
    expect(capturedAuth).toBe('Bearer sheet-token')
    expect(capturedContentType).toBe('application/proto')
    expect(signalPresent).toBe(true)
    expect(signalAbortedDuringFetch).toBe(false)
    expect(decodedRequest).toEqual({ fileId: 'fil_sheet1', index: 1 })
    expect(result).toEqual({
      sheets: ['Checks', 'Next'],
      rows: [['', 'Build passed', '', 'true'], ['Done']],
      rowNumbers: [3, 8],
      truncated: true,
    })
  })

  test('propagates caller AbortSignal cancellation to the ConnectRPC request', async () => {
    saveToken('sheet-token')
    const controller = new AbortController()
    let transportAborted = false

    vi.spyOn(globalThis, 'fetch').mockImplementation(
      (_input, init) =>
        new Promise((_resolve, reject) => {
          init?.signal?.addEventListener(
            'abort',
            () => {
              transportAborted = true
              reject(new DOMException('Aborted', 'AbortError'))
            },
            { once: true },
          )
        }),
    )

    const pending = fetchSheet('fil_sheet1', 0, controller.signal)
    controller.abort()

    await expect(pending).rejects.toThrow()
    expect(transportAborted).toBe(true)
  })

  test('maps ConnectRPC LocalizedError responses to ApiError', async () => {
    saveToken('sheet-token')
    const detailBytes = toBinary(
      LocalizedErrorSchema,
      create(LocalizedErrorSchema, { key: 'filePreview.error' }),
    )
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          code: 'invalid_argument',
          message: 'filePreview.error',
          details: [
            {
              type: LocalizedErrorSchema.typeName,
              value: Buffer.from(detailBytes).toString('base64'),
            },
          ],
        }),
        {
          status: 400,
          headers: { 'content-type': 'application/json' },
        },
      ),
    )

    const controller = new AbortController()
    await expect(fetchSheet('fil_not_xlsx', 0, controller.signal)).rejects.toMatchObject({
      status: 400,
      localized: { key: 'filePreview.error' },
    })
    await expect(fetchSheet('fil_not_xlsx', 0, controller.signal)).rejects.toBeInstanceOf(ApiError)
  })
})
