<template>
  <el-dialog v-model="visible" title="申诉该评价" width="460px" @closed="reset">
    <el-alert
      title="每条评价只能申诉一次；仅评价接收方可以提交。审核通过后将撤销该评价带来的信誉分变化，驳回则分数不变。"
      type="info"
      :closable="false"
      show-icon
      class="appeal-tip"
    />
    <el-form label-width="80px" @submit.prevent>
      <el-form-item label="评价">
        <el-tag size="small">{{ ratingLabel(review?.rating || '') }}</el-tag>
        <span class="review-content">{{ review?.content || '（无文字内容）' }}</span>
      </el-form-item>
      <el-form-item label="申诉理由" required>
        <el-input
          v-model="reason"
          type="textarea"
          :rows="4"
          maxlength="500"
          show-word-limit
          placeholder="请说明申诉理由（2-500字），例如：评价与事实不符、存在恶意差评等"
        />
      </el-form-item>
    </el-form>
    <template #footer>
      <el-button @click="visible = false">取消</el-button>
      <el-button type="primary" :loading="submitting" @click="submit">提交申诉</el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { createAppeal } from '../../api/appeal'
import { ratingLabel } from '../../constants/trade'
import type { Review } from '../../types'

const props = defineProps<{ review: Review | null }>()
const emit = defineEmits<{ (e: 'submitted'): void }>()

const visible = ref(false)
const reason = ref('')
const submitting = ref(false)

function open() {
  visible.value = true
}

function reset() {
  reason.value = ''
  submitting.value = false
}

async function submit() {
  if (!props.review) return
  if (reason.value.trim().length < 2) {
    ElMessage.warning('申诉理由至少 2 个字')
    return
  }
  submitting.value = true
  try {
    await createAppeal({ review_id: props.review.id, reason: reason.value.trim() })
    ElMessage.success('申诉已提交，等待管理员审核')
    visible.value = false
    emit('submitted')
  } finally {
    submitting.value = false
  }
}

defineExpose({ open })
</script>

<style scoped>
.appeal-tip {
  margin-bottom: 12px;
}
.review-content {
  margin-left: 8px;
  color: #606266;
  font-size: 13px;
}
</style>
