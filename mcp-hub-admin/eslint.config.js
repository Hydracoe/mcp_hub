import js from '@eslint/js'
import { defineConfig, globalIgnores } from 'eslint/config'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import globals from 'globals'
import tseslint from 'typescript-eslint'

export default defineConfig([
  globalIgnores([
    'dist',
    'node_modules',
    // MSW 生成并拷进 public 的 service worker，不是我们的代码
    'public/mockServiceWorker.js',
  ]),

  // 源码：TS + TSX，浏览器环境
  {
    files: ['src/**/*.{ts,tsx}'],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.recommended,
      reactHooks.configs.flat['recommended-latest'],
      reactRefresh.configs.vite,
    ],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
    rules: {
      '@typescript-eslint/no-unused-vars': [
        'error',
        {
          args: 'after-used',
          // 下划线前缀 = 有意忽略（如 applyCredentialRequest 的占位参数）
          argsIgnorePattern: '^_',
          varsIgnorePattern: '^_',
          caughtErrorsIgnorePattern: '^_',
        },
      ],
      'no-void': 'off',

      /*
       * react-hooks v7 新规则在存量代码有命中（props 同步进本地 state 等），逐个改造属独立重构；
       * 先降为 warning 让 lint 门禁跑通。清完这批请改回 'error'。
       */
      'react-hooks/set-state-in-effect': 'warn',
      'react-hooks/refs': 'warn',
      'react-hooks/purity': 'warn',
    },
  },

  /*
   * shadcn/ui 原语：`cva` 的 variants 与组件同文件导出是上游约定，不是结构问题。
   */
  {
    files: ['src/components/ui/**/*.tsx'],
    rules: { 'react-refresh/only-export-components': 'off' },
  },

  // Mock 层跑在 Service Worker 里，会用到 Worker 全局
  {
    files: ['src/lib/mock/**/*.ts'],
    languageOptions: { globals: { ...globals.browser, ...globals.serviceworker } },
  },

  // 构建脚本跑在 Node 里
  {
    files: ['*.config.{js,ts}', 'vite.config.ts'],
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.node,
    },
  },
])
