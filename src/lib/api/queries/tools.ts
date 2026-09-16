import { useQuery } from '@tanstack/react-query'

import * as api from '../endpoints'
import { queryKeys } from '../query-keys'

export function useTools(params?: { serverKey?: string; q?: string }) {
  return useQuery({
    queryKey: queryKeys.tools(params),
    queryFn: () => api.listTools(params),
  })
}
