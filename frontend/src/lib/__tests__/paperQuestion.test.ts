import { describe, expect, it } from 'vitest'
import { createQuestionNode, mergeQuestionProjections } from '../paperQuestion'
import type { PaperQuestionProjection } from '@/api/paper'

function projection(id: number, revision: number): PaperQuestionProjection {
  return { id, content_revision: revision } as PaperQuestionProjection
}

describe('paperQuestion', () => {
  it('creates a question node with the frozen revision', () => {
    const node = createQuestionNode('node-1', 42, 7)
    expect(node.node_type).toBe('question')
    expect(node.content).toEqual({ question_id: 42, content_revision: 7 })
  })

  it('merges newly loaded projections without dropping existing ones', () => {
    const merged = mergeQuestionProjections(
      { 1: projection(1, 1) },
      { 2: projection(2, 2) },
    )
    expect(Object.keys(merged)).toEqual(['1', '2'])
    expect(merged[2].content_revision).toBe(2)
  })
})
