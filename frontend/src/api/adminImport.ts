import { downloadFile, get, post } from './http'

export type AdminImportKind = 'users' | 'tags' | 'knowledge_points'
export type KnowledgePointImportMode = 'incremental' | 'rebuild'

export interface AdminImportRowError {
  row: number
  message: string
}

export interface UserImportResult {
  status: 'success' | 'partial' | 'failed'
  total: number
  created: number
  failed: number
  errors: AdminImportRowError[]
}

export interface TagImportResult extends UserImportResult {
  skipped: number
}

export interface KnowledgePointImportPreflight {
  subject_id: number
  subject_name: string
  existing_count: number
  affected_questions: number
}

export interface KnowledgePointImportResult {
  status: 'success' | 'partial' | 'failed'
  mode: KnowledgePointImportMode
  total: number
  created: number
  skipped: number
  failed: number
  vector_synced: boolean
  errors: AdminImportRowError[]
}

const TEMPLATE_NAMES: Record<AdminImportKind, string> = {
  users: 'users_template.xlsx',
  tags: 'tags_template.xlsx',
  knowledge_points: 'knowledge_points_template.xlsx',
}

function templateUrl(kind: AdminImportKind, subjectId?: number): string {
  if (kind === 'users') return '/users/import-template'
  if (!subjectId) throw new Error('缺少学科')
  if (kind === 'tags') return `/subjects/${subjectId}/tags/import-template`
  return `/subjects/${subjectId}/knowledge-points/import-template`
}

export const adminImportApi = {
  downloadTemplate(kind: AdminImportKind, subjectId?: number) {
    return downloadFile(templateUrl(kind, subjectId), undefined, TEMPLATE_NAMES[kind])
  },

  importUsers(file: File) {
    const form = new FormData()
    form.append('file', file)
    return post<UserImportResult>('/users/import', form)
  },

  importTags(subjectId: number, file: File) {
    const form = new FormData()
    form.append('file', file)
    return post<TagImportResult>(`/subjects/${subjectId}/tags/import`, form)
  },

  knowledgePointPreflight(subjectId: number) {
    return get<KnowledgePointImportPreflight>(`/subjects/${subjectId}/knowledge-points/import-preflight`)
  },

  importKnowledgePoints(
    subjectId: number,
    file: File,
    mode: KnowledgePointImportMode,
  ) {
    const form = new FormData()
    form.append('file', file)
    form.append('mode', mode)
    return post<KnowledgePointImportResult>(
      `/subjects/${subjectId}/knowledge-points/import`,
      form,
    )
  },
}

