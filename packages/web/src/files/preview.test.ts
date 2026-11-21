import { describe, expect, test } from 'vitest'
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
