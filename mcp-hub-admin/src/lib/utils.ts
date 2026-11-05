import { clsx, type ClassValue } from 'clsx'
import { twMerge } from 'tailwind-merge'

/** 合并条件类名并消解 Tailwind 冲突（后写的赢）。 */
export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}
