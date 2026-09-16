import { useEffect, useMemo, useState } from 'react'
import {
  addDays,
  addHours,
  addMinutes,
  addMonths,
  eachDayOfInterval,
  endOfDay,
  format,
  isSameDay,
  isSameMonth,
  setHours,
  setMinutes,
  startOfDay,
  startOfMonth,
  startOfWeek,
  subMonths,
} from 'date-fns'
import { zhCN } from 'date-fns/locale'
import { CalendarDays, ChevronLeft, ChevronRight, X } from 'lucide-react'

import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export interface DateTimeRange {
  since?: number
  until?: number
}

export interface DateTimeRangePickerProps extends DateTimeRange {
  onChange: (next: DateTimeRange) => void
  className?: string
  placeholder?: string
  'aria-label'?: string
}

/* 周一为一周之首（中文习惯）。 */
const WEEK_OPTS = { weekStartsOn: 1 as const }
const WEEKDAYS = ['一', '二', '三', '四', '五', '六', '日']

/** 快捷区间：一次点击即生效并关闭，预设本身就是完整答案。 */
const PRESETS: { label: string; range: () => DateTimeRange }[] = [
  { label: '最近 5 分钟', range: () => ({ since: +addMinutes(Date.now(), -5), until: Date.now() }) },
  { label: '最近 1 小时', range: () => ({ since: +addHours(Date.now(), -1), until: Date.now() }) },
  { label: '最近 24 小时', range: () => ({ since: +addHours(Date.now(), -24), until: Date.now() }) },
  { label: '今天', range: () => ({ since: +startOfDay(new Date()), until: Date.now() }) },
  {
    label: '昨天',
    range: () => {
      const y = addDays(new Date(), -1)
      return { since: +startOfDay(y), until: +endOfDay(y) }
    },
  },
  {
    label: '最近 7 天',
    range: () => ({ since: +startOfDay(addDays(new Date(), -6)), until: Date.now() }),
  },
]

/** ms → `HH:mm`。 */
function hhmm(ms: number): string {
  return format(ms, 'HH:mm')
}

/** 把 `HH:mm` 贴到某一天的日期上。 */
function atTime(day: Date, hm: string): number {
  const [h, m] = hm.split(':').map(Number)
  return +setMinutes(setHours(day, h || 0), m || 0)
}

/** 触发器上的区间文案。同年省略年份，够窄也不至于被截得看不懂。 */
function formatRange({ since, until }: DateTimeRange, now = new Date()): string {
  if (!since && !until) return '全部时间'
  const sameYear = (ms: number) => new Date(ms).getFullYear() === now.getFullYear()
  const f = (ms: number) => format(ms, sameYear(ms) ? 'MM-dd HH:mm' : 'yyyy-MM-dd HH:mm')
  if (since && !until) return `${f(since)} 起`
  if (!since && until) return `至 ${f(until)}`
  return `${f(since!)} → ${f(until!)}`
}

/** 时间区间选择器。面板内部维护草稿，点「应用」才写回；预设一键生效；
 *  since > until 时挡住提交。 */
export function DateTimeRangePicker({
  since,
  until,
  onChange,
  className,
  placeholder = '全部时间',
  'aria-label': ariaLabel,
}: DateTimeRangePickerProps) {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState<DateTimeRange>({ since, until })
  const [cursor, setCursor] = useState(() => new Date())

  /* 每次打开都从当前值同步草稿 —— 外部（URL / 清空筛选）改了值，
     面板里不能还留着上一次的残留。 */
  useEffect(() => {
    if (!open) return
    setDraft({ since, until })
    setCursor(new Date(since ?? Date.now()))
  }, [open, since, until])

  const startDay = draft.since ? new Date(draft.since) : null
  const endDay = draft.until ? new Date(draft.until) : null
  const startHM = draft.since ? hhmm(draft.since) : '00:00'
  const endHM = draft.until ? hhmm(draft.until) : '23:59'

  const invalid = Boolean(draft.since && draft.until && draft.since > draft.until)

  /* 固定 6 行 42 格：月份之间切换时网格高度不跳。 */
  const grid = useMemo(() => {
    const from = startOfWeek(startOfMonth(cursor), WEEK_OPTS)
    return eachDayOfInterval({ start: from, end: addDays(from, 41) })
  }, [cursor])

  /** 点日历：没选完就续上，选完了就重开一段。 */
  const clickDay = (day: Date) => {
    if (startDay && !endDay && day >= startOfDay(startDay)) {
      setDraft((d) => ({ ...d, until: atTime(day, endHM) }))
      return
    }
    setDraft({ since: atTime(day, startHM), until: undefined })
  }

  const apply = () => {
    onChange({ since: draft.since, until: draft.until })
    setOpen(false)
  }

  const clear = () => {
    onChange({ since: undefined, until: undefined })
    setOpen(false)
  }

  const hasValue = Boolean(since || until)

  return (
    <Popover open={open} onOpenChange={setOpen}>
      {/* 清除按钮**不能**嵌在触发器里 —— button 里套 button 是非法 HTML，
          读屏软件与键盘都会出怪事。这里摆在旁边，靠绝对定位压在同一格上。 */}
      <div className={cn('relative inline-flex min-w-0', className)}>
        <PopoverTrigger asChild>
          <button
            type="button"
            aria-label={ariaLabel ?? '时间范围'}
            data-placeholder={hasValue ? undefined : ''}
            className="ui-select-trigger flex w-full items-center gap-2"
          >
            <CalendarDays className="size-3.5 shrink-0 text-icon-muted" />
            <span className="min-w-0 flex-1 truncate text-left tabular-nums">
              {hasValue ? formatRange({ since, until }) : placeholder}
            </span>
            {hasValue ? <span className="w-3.5 shrink-0" aria-hidden /> : null}
          </button>
        </PopoverTrigger>
        {hasValue ? (
          <button
            type="button"
            aria-label="清除时间范围"
            onClick={() => onChange({ since: undefined, until: undefined })}
            className="absolute top-1/2 right-1.5 -translate-y-1/2 rounded-full p-0.5 text-icon-muted hover:bg-fill-secondary hover:text-foreground"
          >
            <X className="size-3.5" />
          </button>
        ) : null}
      </div>

      <PopoverContent align="end" className="ui-popover w-[312px] p-0">
        <div className="flex flex-wrap gap-1.5 border-b border-separator p-3">
          {PRESETS.map((p) => (
            <button
              key={p.label}
              type="button"
              className="rounded-full bg-muted px-2.5 py-1 text-[12px] font-medium text-foreground transition-colors hover:bg-fill-secondary"
              onClick={() => {
                onChange(p.range())
                setOpen(false)
              }}
            >
              {p.label}
            </button>
          ))}
        </div>

        <div className="p-3">
          <div className="mb-2 flex items-center justify-between">
            <button
              type="button"
              aria-label="上个月"
              className="rounded-md p-1 text-icon-muted hover:bg-fill-secondary hover:text-foreground"
              onClick={() => setCursor((c) => subMonths(c, 1))}
            >
              <ChevronLeft className="size-4" />
            </button>
            <span className="font-mono text-[12.5px] tabular-nums text-foreground">
              {format(cursor, 'yyyy 年 M 月', { locale: zhCN })}
            </span>
            <div className="flex items-center">
              <button
                type="button"
                className="mr-0.5 rounded-md px-1.5 py-1 text-[11.5px] text-link hover:bg-fill-secondary"
                onClick={() => setCursor(new Date())}
              >
                今天
              </button>
              <button
                type="button"
                aria-label="下个月"
                className="rounded-md p-1 text-icon-muted hover:bg-fill-secondary hover:text-foreground"
                onClick={() => setCursor((c) => addMonths(c, 1))}
              >
                <ChevronRight className="size-4" />
              </button>
            </div>
          </div>

          <div className="grid grid-cols-7 gap-0.5">
            {WEEKDAYS.map((w) => (
              <span
                key={w}
                className="grid h-6 place-items-center text-[11px] font-medium text-muted-foreground"
              >
                {w}
              </span>
            ))}
            {grid.map((d) => {
              const isStart = startDay ? isSameDay(d, startDay) : false
              const isEnd = endDay ? isSameDay(d, endDay) : false
              const inRange =
                startDay && endDay
                  ? d >= startOfDay(startDay) && d <= startOfDay(endDay)
                  : false
              const outside = !isSameMonth(d, cursor)
              return (
                <button
                  key={+d}
                  type="button"
                  onClick={() => clickDay(d)}
                  aria-pressed={isStart || isEnd}
                  className={cn(
                    'grid h-7 place-items-center rounded-md font-mono text-[12px] tabular-nums transition-colors',
                    'hover:bg-fill-secondary',
                    outside && 'text-icon-muted',
                    inRange && !isStart && !isEnd && 'bg-selection text-foreground',
                    isStart || isEnd
                      ? 'bg-brand-strong font-semibold text-white hover:bg-brand-strong'
                      : !outside && 'text-foreground',
                  )}
                >
                  {d.getDate()}
                </button>
              )
            })}
          </div>

          <div className="mt-3 grid grid-cols-2 gap-2">
            <label className="flex items-center gap-2">
              <span className="shrink-0 text-[11.5px] text-muted-foreground">开始</span>
              <input
                type="time"
                value={startHM}
                disabled={!draft.since}
                onChange={(e) =>
                  setDraft((s) =>
                    s.since ? { ...s, since: atTime(new Date(s.since), e.target.value) } : s,
                  )
                }
                className="ui-time-input flex-1 disabled:opacity-50"
                aria-label="开始时间"
              />
            </label>
            <label className="flex items-center gap-2">
              <span className="shrink-0 text-[11.5px] text-muted-foreground">结束</span>
              <input
                type="time"
                value={endHM}
                disabled={!draft.until}
                onChange={(e) =>
                  setDraft((s) =>
                    s.until ? { ...s, until: atTime(new Date(s.until), e.target.value) } : s,
                  )
                }
                className="ui-time-input flex-1 disabled:opacity-50"
                aria-label="结束时间"
              />
            </label>
          </div>

          <p className="mt-2 text-[11.5px] text-muted-foreground">
            {!draft.since
              ? '在日历上点一天作为起点，再点一天作为终点。'
              : !draft.until
                ? '已选起点，再点一天作为终点。'
                : formatRange(draft)}
          </p>

          {invalid ? (
            <p className="mt-1.5 text-[11.5px] text-error-strong">
              开始时间晚于结束时间，查询会返回空结果。
            </p>
          ) : null}
        </div>

        <div className="flex items-center justify-between gap-2 border-t border-separator bg-muted px-3 py-2.5">
          <Button variant="ghost" size="sm" onClick={clear} disabled={!draft.since && !draft.until}>
            清除
          </Button>
          <div className="flex items-center gap-2">
            <Button variant="ghost" size="sm" onClick={() => setOpen(false)}>
              取消
            </Button>
            <Button variant="primary" size="sm" onClick={apply} disabled={invalid}>
              应用
            </Button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}
