import { describe, expect, it } from 'vitest'
import { appendQuestionNodes, createQuestionNode, mergeQuestionProjections } from '../paperQuestion'
import type { PaperNode, PaperQuestionProjection } from '@/api/paper'

function projection(id: number, revision: number): PaperQuestionProjection {
  return { id, content_revision: revision } as PaperQuestionProjection
}

describe('paperQuestion', () => {
  it('creates a question node with the frozen revision', () => {
    const node = createQuestionNode('node-1', 42, 7)
    expect(node.node_type).toBe('question')
    expect(node.content).toEqual({ question_id: 42, content_revision: 7 })
    expect(node.props).toEqual({})
  })

  it('carries blueprint score into node props', () => {
    const node = createQuestionNode('node-1', 42, 7, { score: 2.5 })
    expect(node.props).toEqual({ score: 2.5 })
  })

  it('merges newly loaded projections without dropping existing ones', () => {
    const merged = mergeQuestionProjections(
      { 1: projection(1, 1) },
      { 2: projection(2, 2) },
    )
    expect(Object.keys(merged)).toEqual(['1', '2'])
    expect(merged[2].content_revision).toBe(2)
  })

  it('appends selected questions before the answer module', () => {
    const before = [
      createQuestionNode('q1', 1, 1),
      {
        ...createQuestionNode('module', 0, 0),
        node_type: 'question_details',
        position: 1,
      },
    ] as PaperNode[]
    const after = appendQuestionNodes(before, [2], { 2: projection(2, 4) }, () => 'q2')
    expect(after.map((node) => node.id)).toEqual(['q1', 'q2', 'module'])
    expect(after[1].content).toEqual({ question_id: 2, content_revision: 4 })
    expect(after.map((node) => node.position)).toEqual([0, 1, 2])
  })
})
