<template>
  <!-- 设置弹窗(Teleport 到 body,避免被顶部栏 overflow 裁剪):
       收纳主题切换与界面语言切换,由右上角 ⚙ 按钮打开 -->
  <Teleport to="body">
    <div
      v-if="open"
      class="settings-overlay"
      role="dialog"
      aria-modal="true"
      :aria-label="t('settings.title')"
      @mousedown.self="close"
      @keydown.esc.window="close"
    >
      <div class="settings-dialog">
        <div class="settings-header">
          <span class="settings-title">{{ t('settings.title') }}</span>
          <button class="settings-close" :title="t('settings.close')" @click="close">✕</button>
        </div>

        <div class="settings-body">
          <!-- 主题:深色 / 浅色 -->
          <div class="settings-section">
            <div class="settings-label">{{ t('settings.theme') }}</div>
            <div class="settings-options">
              <button
                class="option-btn"
                :class="{ active: theme === 'dark' }"
                @click="selectTheme('dark')"
              >☾ {{ t('settings.dark') }}</button>
              <button
                class="option-btn"
                :class="{ active: theme === 'light' }"
                @click="selectTheme('light')"
              >☀ {{ t('settings.light') }}</button>
            </div>
          </div>

          <!-- 语言:中文 / English -->
          <div class="settings-section">
            <div class="settings-label">{{ t('settings.language') }}</div>
            <div class="settings-options">
              <button
                class="option-btn"
                :class="{ active: lang === 'zh' }"
                @click="selectLang('zh')"
              >中文</button>
              <button
                class="option-btn"
                :class="{ active: lang === 'en' }"
                @click="selectLang('en')"
              >English</button>
            </div>
          </div>

          <!-- 页面标题:部署级设置,服务端持久化,显示在浏览器标签页 -->
          <div class="settings-section">
            <div class="settings-label">{{ t('settings.pageTitle') }}</div>
            <div class="title-row">
              <input
                v-model="titleDraft"
                class="title-input"
                type="text"
                maxlength="200"
                :placeholder="t('settings.pageTitlePlaceholder')"
                @keydown.enter="saveTitle"
              />
              <button
                class="option-btn option-btn-save"
                :disabled="savingTitle"
                @click="saveTitle"
              >{{ t('settings.save') }}</button>
            </div>
            <div v-if="titleStatus" class="title-status" :class="titleStatusOk ? 'title-ok' : 'title-err'">
              {{ titleStatus }}
            </div>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { lang, setLang, t } from '../utils/i18n'
import { getPageTitle, setPageTitle } from '../utils/api'
import type { Theme } from '../utils/theme'

const props = defineProps<{
    // 弹窗是否可见(由 App 控制)
    open: boolean
    // 当前主题(驱动选项高亮;实际应用由 App 完成并回传)
    theme: Theme
}>()

const emit = defineEmits<{
    (e: 'close'): void
    // 请求切换主题(目标主题),App 负责 applyTheme + notifyThemeChange
    (e: 'theme', theme: Theme): void
    // 页面标题已保存(服务端规范化后的值),App 负责应用 document.title
    (e: 'title-saved', title: string): void
}>()

function close() {
    emit('close')
}

// 选择主题:与当前不同才上报(避免无谓重渲染)
function selectTheme(theme: Theme) {
    if (theme !== props.theme) emit('theme', theme)
}

// 选择语言:setLang 是全局响应式状态,界面文案即时更新
function selectLang(l: 'zh' | 'en') {
    if (l !== lang.value) setLang(l)
}

// ── 页面标题 ──
const titleDraft = ref('')
const savingTitle = ref(false)
const titleStatus = ref('')
const titleStatusOk = ref(true)

// 每次打开弹窗时拉取服务端当前值作为草稿;拉取失败静默(按未设置处理)
watch(
    () => props.open,
    async (open) => {
        if (!open) return
        titleDraft.value = ''
        titleStatus.value = ''
        try {
            titleDraft.value = await getPageTitle()
        } catch {
            // 服务端不可用/旧版本:忽略
        }
    },
)

// 保存:PUT 到服务端(部署级持久化),成功后上报 App 应用 document.title
async function saveTitle() {
    if (savingTitle.value) return
    savingTitle.value = true
    titleStatus.value = ''
    try {
        const saved = await setPageTitle(titleDraft.value.trim())
        emit('title-saved', saved)
        titleStatusOk.value = true
        titleStatus.value = t('settings.saved')
    } catch {
        titleStatusOk.value = false
        titleStatus.value = t('settings.saveFailed')
    } finally {
        savingTitle.value = false
    }
}
</script>

<style scoped>
/* ── 覆盖层:点击空白处关闭 ── */
.settings-overlay {
    position: fixed;
    inset: 0;
    z-index: 1000;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--overlay);
}

/* ── 对话框(仿 VSCode 风格,与断开弹窗一致) ── */
.settings-dialog {
    width: 320px;
    max-width: calc(100vw - 32px);
    background: var(--bg-dialog);
    border: 1px solid var(--border-dialog);
    border-radius: 6px;
    box-shadow: 0 8px 24px rgba(0, 0, 0, 0.5);
    display: flex;
    flex-direction: column;
    overflow: hidden;
}

.settings-header {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 12px 14px;
    border-bottom: 1px solid var(--border-tab);
}

.settings-title {
    font-size: 14px;
    font-weight: 600;
    /* 固定行高:中文字体默认行高(normal)大于拉丁字体,
       不固定会导致切换中英文时弹窗高度/位置跳动 */
    line-height: 1.4;
    color: var(--fg-bright);
}

.settings-close {
    background: none;
    border: none;
    color: var(--fg-dim);
    font-size: 13px;
    line-height: 1;
    padding: 3px 6px;
    border-radius: 3px;
    cursor: pointer;
}

.settings-close:hover {
    background: var(--bg-tab-hover);
    color: var(--fg-bright);
}

.settings-body {
    display: flex;
    flex-direction: column;
    gap: 18px;
    padding: 16px 14px 18px;
}

.settings-label {
    font-size: 12px;
    /* 固定行高:避免中英文切换时行盒高度不同(同 .settings-title) */
    line-height: 1.4;
    color: var(--fg-muted);
    margin-bottom: 8px;
}

/* ── 分段选项(当前项高亮) ── */
.settings-options {
    display: flex;
    gap: 8px;
}

.option-btn {
    flex: 1 1 0;
    padding: 6px 0;
    background: var(--bg-tab);
    border: 1px solid var(--border-tab);
    border-radius: 4px;
    color: var(--fg-dim);
    font-size: 13px;
    font-family: inherit;
    cursor: pointer;
    line-height: 1.4;
    transition: background 0.15s, border-color 0.15s, color 0.15s;
}

.option-btn:hover {
    background: var(--bg-tab-hover);
    color: var(--fg-bright);
}

.option-btn.active {
    background: var(--bg-tab-active);
    border-color: var(--accent);
    color: var(--fg-bright);
}

.option-btn:disabled {
    opacity: 0.6;
    cursor: default;
}

/* ── 页面标题:输入框 + 保存 ── */
.title-row {
    display: flex;
    gap: 8px;
}

.title-input {
    flex: 1 1 auto;
    min-width: 0;
    height: 30px;
    padding: 0 8px;
    background: var(--bg-input);
    border: 1px solid var(--border-tab);
    border-radius: 4px;
    color: var(--fg);
    font-size: 13px;
    font-family: inherit;
    line-height: 1.4;
}

.title-input::placeholder {
    color: var(--fg-hint);
}

.title-input:focus {
    outline: none;
    border-color: var(--accent);
}

.option-btn-save {
    flex: 0 0 auto;
    height: 30px;
    padding: 0 14px;
}

.title-status {
    font-size: 12px;
    /* 固定行高:避免中英文切换时弹窗高度/位置跳动 */
    line-height: 1.4;
    margin-top: 6px;
}

.title-ok {
    color: var(--net-good);
}

.title-err {
    color: var(--net-bad);
}
</style>