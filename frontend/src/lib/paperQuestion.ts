import { questionApi } from '@/api'
import type { QuestionDetail } from '@/api/types'
import type { PaperNode, PaperQuestionProjection, QuestionRef } from '@/api/paper'

function emptyRichDoc(): { type: 'doc'; content: unknown[] } {
  return { type: 'doc', content: [] }
}

export function toPaperProjection(detail: QuestionDetail): PaperQuestionProjection {
  return {
    id: detail.id,
    question_type: detail.question_type,
    question_type_label: detail.question_type_label,
    difficulty: detail.difficulty,
    stem: detail.stem,
    options: (detail.options ?? []).map((o) => ({
      id: o.id,
      label: o.label,
      content: o.content ?? emptyRichDoc(),
    })),
    answer: {
      kind: detail.answer.kind,
      correct: detail.answer.correct,
      bool_answer: detail.answer.bool_answer,
      blanks: detail.answer.blanks,
      reference: detail.answer.reference ?? null,
    },
    analysis: detail.analysis,
    solution: detail.solution,
    summary: detail.summary,
    stem_text: detail.stem_text,
    content_revision: detail.content_revision,
    stale: false,
  }
}

export function mergeQuestionProjections(
  existing: Record<number, PaperQuestionProjection>,
  additions: Record<number, PaperQuestionProjection>,
): Record<number, PaperQuestionProjection> {
  return { ...existing, ...additions }
}

export function createQuestionNode(id: string, questionID: number, contentRevision: number): PaperNode {
  const content: QuestionRef = { question_id: questionID, content_revision: contentRevision }
  return {
    id,
    parent_id: null,
    position: 0,
    node_kind: 'block',
    node_type: 'question',
    content,
    props: {},
    schema_version: 1,
    source_question_node_id: null,
    anchor_before_node_id: null,
  }
}

export function appendQuestionNodes(
  before: PaperNode[],
  questionIDs: number[],
  projections: Record<number, PaperQuestionProjection>,
  nextID: () => string,
): PaperNode[] {
  const roots = before
    .filter((node) => node.parent_id === null && node.node_kind !== 'reference')
    .slice()
    .sort((a, b) => a.position - b.position)
  const moduleIndex = roots.findIndex((node) => node.node_type === 'question_details')
  const at = moduleIndex >= 0 ? moduleIndex : roots.length
  const taken = new Set(before.map((node) => node.id))
  const additions: PaperNode[] = []
  for (const id of questionIDs) {
    const projection = projections[id]
    if (!projection) continue
    let nodeID = nextID()
    while (taken.has(nodeID)) nodeID = nextID()
    taken.add(nodeID)
    additions.push(createQuestionNode(nodeID, id, projection.content_revision))
  }
  const top = [...roots.slice(0, at), ...additions, ...roots.slice(at)]
  const topIDs = new Set(top.map((node) => node.id))
  return [
    ...top.map((node, index) => ({ ...node, position: index })),
    ...before.filter((node) => node.parent_id !== null && !topIDs.has(node.id)),
  ]
}

export async function loadPaperQuestionProjections(
  ids: number[],
): Promise<Record<number, PaperQuestionProjection>> {
  const unique = [...new Set(ids.filter((id) => Number.isInteger(id) && id > 0))]
  if (unique.length === 0) return {}
  const details = await questionApi.batchRead(unique)
  const out: Record<number, PaperQuestionProjection> = {}
  for (const detail of details) out[detail.id] = toPaperProjection(detail)
  return out
}
