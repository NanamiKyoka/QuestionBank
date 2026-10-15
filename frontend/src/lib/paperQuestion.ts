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
