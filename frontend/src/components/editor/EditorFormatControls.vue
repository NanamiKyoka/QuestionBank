<script setup lang="ts">
import { computed } from 'vue'
import type { Editor } from '@tiptap/core'
import {
  redo,
  runTableCommand,
  setAlignment,
  setParagraphStyle,
  undo,
  type TableCommand,
} from './richEditorActions'

const props = defineProps<{ editor: Editor }>()

const headingValue = computed(() => {
  for (const level of [1, 2, 3] as const) {
    if (props.editor.isActive('heading', { level })) return String(level)
  }
  return '0'
})

function onHeading(value: string | number | boolean | object) {
  setParagraphStyle(props.editor, Number(value))
}

function onTable(command: string | number | boolean | object) {
  runTableCommand(props.editor, String(command) as TableCommand)
}
</script>

<template>
  <el-button-group>
    <el-button size="small" title="撤销" aria-label="撤销" :disabled="!editor.can().undo()" @click="undo(editor)">
      ↶
    </el-button>
    <el-button size="small" title="重做" aria-label="重做" :disabled="!editor.can().redo()" @click="redo(editor)">
      ↷
    </el-button>
  </el-button-group>

  <el-dropdown class="ml-1" @command="onHeading">
    <el-button size="small">
      {{ headingValue === '0' ? '正文' : `${headingValue} 级标题` }}
    </el-button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item command="0">正文</el-dropdown-item>
        <el-dropdown-item command="1">一级标题</el-dropdown-item>
        <el-dropdown-item command="2">二级标题</el-dropdown-item>
        <el-dropdown-item command="3">三级标题</el-dropdown-item>
      </el-dropdown-menu>
    </template>
  </el-dropdown>

  <el-button-group class="ml-1">
    <el-button
      size="small"
      title="左对齐"
      aria-label="左对齐"
      :type="editor.isActive({ textAlign: 'left' }) ? 'primary' : 'default'"
      @click="setAlignment(editor, 'left')"
    >
      左
    </el-button>
    <el-button
      size="small"
      title="居中"
      aria-label="居中"
      :type="editor.isActive({ textAlign: 'center' }) ? 'primary' : 'default'"
      @click="setAlignment(editor, 'center')"
    >
      中
    </el-button>
    <el-button
      size="small"
      title="右对齐"
      aria-label="右对齐"
      :type="editor.isActive({ textAlign: 'right' }) ? 'primary' : 'default'"
      @click="setAlignment(editor, 'right')"
    >
      右
    </el-button>
  </el-button-group>

  <el-dropdown class="ml-1" @command="onTable">
    <el-button size="small" :type="editor.isActive('table') ? 'primary' : 'default'">
      表格
    </el-button>
    <template #dropdown>
      <el-dropdown-menu>
        <el-dropdown-item command="insert">插入 3 × 3 表格</el-dropdown-item>
        <template v-if="editor.isActive('table')">
          <el-dropdown-item divided command="addRowBefore">上方插入行</el-dropdown-item>
          <el-dropdown-item command="addRowAfter">下方插入行</el-dropdown-item>
          <el-dropdown-item command="deleteRow">删除当前行</el-dropdown-item>
          <el-dropdown-item divided command="addColumnBefore">左侧插入列</el-dropdown-item>
          <el-dropdown-item command="addColumnAfter">右侧插入列</el-dropdown-item>
          <el-dropdown-item command="deleteColumn">删除当前列</el-dropdown-item>
          <el-dropdown-item divided command="mergeCells">合并选中单元格</el-dropdown-item>
          <el-dropdown-item command="splitCell">拆分当前单元格</el-dropdown-item>
          <el-dropdown-item divided command="deleteTable">删除表格</el-dropdown-item>
        </template>
      </el-dropdown-menu>
    </template>
  </el-dropdown>
</template>
