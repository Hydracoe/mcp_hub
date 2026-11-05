import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import * as api from '../endpoints'
import { queryKeys } from '../query-keys'

export function useCategories() {
  return useQuery({ queryKey: queryKeys.categories, queryFn: api.listCategories })
}

export function useCategoryMutations() {
  const qc = useQueryClient()
  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: queryKeys.categories })
    void qc.invalidateQueries({ queryKey: queryKeys.servers })
    // 分类信息影响工具列表的分类字段与检索 facet，工具市场与概览
    // 里的计数都会跟着变，不失效就会显示旧数据。对外工具名不受影响。
    void qc.invalidateQueries({ queryKey: ['tools'] })
    void qc.invalidateQueries({ queryKey: queryKeys.overview })
  }

  const create = useMutation({
    mutationFn: (body: api.CategoryRequest) => api.createCategory(body),
    onSuccess: invalidate,
  })
  const update = useMutation({
    mutationFn: (vars: { id: string; body: api.CategoryRequest }) =>
      api.updateCategory(vars.id, vars.body),
    onSuccess: invalidate,
  })
  const remove = useMutation({
    mutationFn: (id: string) => api.deleteCategory(id),
    onSuccess: invalidate,
  })

  return { create, update, remove }
}
