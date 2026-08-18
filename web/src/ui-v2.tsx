import React from 'react'
import { Dialog } from '@ark-ui/react/dialog'
import { Portal } from '@ark-ui/react/portal'
import { Tabs } from '@ark-ui/react/tabs'
import { Switch } from '@ark-ui/react/switch'
import { motion, useReducedMotion } from 'motion/react'
import {
  Archive,
  Activity,
  Bell,
  Box,
  CalendarClock,
  Check,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Command,
  Database,
  Code2,
  Download,
  FileArchive,
  FileCog,
  Film,
  Folder,
  GitCompare,
  Globe2,
  HeartPulse,
  HardDrive,
  History as HistoryIcon,
  Layers,
  LayoutDashboard,
  ListTodo,
  LockKeyhole,
  LogOut,
  MoreHorizontal,
  Network,
  Package,
  Palette,
  Plug,
  Plus,
  RotateCcw,
  ScanLine,
  Search,
  Server,
  ShieldAlert,
  ShieldCheck,
  SlidersHorizontal,
  Settings as SettingsIcon,
  SquareTerminal,
  Timer,
  Trash2,
  UploadCloud,
  UserPlus,
  Users as UsersIcon,
  Workflow,
  X,
  type LucideIcon,
} from 'lucide-react'

const iconMap:Record<string,LucideIcon> = {
  dashboard: LayoutDashboard,
  hosts: Server,
  containers: Box,
  database: Database,
  media: Film,
  network: Network,
  activity: Activity,
  storage: HardDrive,
  web: Globe2,
  backups: FileCog,
  rollback: RotateCcw,
  chains: Workflow,
  automation: Timer,
  jobs: ListTodo,
  history: HistoryIcon,
  logs: SquareTerminal,
  users: UsersIcon,
  settings: SettingsIcon,
  check: Check,
  bell: Bell,
  plus: Plus,
  folder: Folder,
  scan: ScanLine,
  shield: ShieldCheck,
  'shield-alert': ShieldAlert,
  heartbeat: HeartPulse,
  'file-archive': FileArchive,
  compare: GitCompare,
  layers: Layers,
  calendar: CalendarClock,
  trash: Trash2,
  'user-plus': UserPlus,
  sliders: SlidersHorizontal,
  package: Package,
  palette: Palette,
  plug: Plug,
  lock: LockKeyhole,
  download: Download,
  archive: Archive,
  search: Search,
  command: Command,
  code: Code2,
  upload: UploadCloud,
  logout: LogOut,
  close: X,
  previous: ChevronLeft,
  next: ChevronRight,
  down: ChevronDown,
  more: MoreHorizontal,
}

export function Icon({name,className=''}:{name:string,className?:string}){
  const Glyph=iconMap[name]||Box
  return <Glyph aria-hidden="true" className={`v2-lucide ${className}`} strokeWidth={2}/>
}

export function PageMotion({children,page}:{children:React.ReactNode,page:string}){
  const reduce=useReducedMotion()
  return <motion.div
    key={page}
    className="v2-page-motion"
    initial={reduce?false:{opacity:0,y:8}}
    animate={{opacity:1,y:0}}
    transition={{duration:reduce?0:.2,ease:[.2,.8,.2,1]}}
  >{children}</motion.div>
}

export function Modal({open,onClose,title,description,children,wide=false,extraWide=false}:any){
  const reduce=useReducedMotion()
  return <Dialog.Root open={!!open} onOpenChange={e=>{if(!e.open)onClose?.()}} lazyMount unmountOnExit>
    <Portal>
      <Dialog.Backdrop className="v2-dialog-backdrop"/>
      <Dialog.Positioner className="v2-dialog-positioner">
        <Dialog.Content className={`v2-dialog-content ${extraWide?'is-extra-wide':wide?'is-wide':''}`}>
          <motion.div initial={reduce?false:{opacity:0,y:10,scale:.99}} animate={{opacity:1,y:0,scale:1}} transition={{duration:reduce?0:.18,ease:[.2,.8,.2,1]}}>
            <div className="v2-dialog-header">
              <div className="min-w-0">
                <Dialog.Title className="v2-dialog-title">{title}</Dialog.Title>
                {description?<Dialog.Description className="v2-dialog-description">{description}</Dialog.Description>:null}
              </div>
              <Dialog.CloseTrigger className="v2-icon-button" aria-label="Close"><Icon name="close"/></Dialog.CloseTrigger>
            </div>
            <div className="v2-dialog-body">{children}</div>
          </motion.div>
        </Dialog.Content>
      </Dialog.Positioner>
    </Portal>
  </Dialog.Root>
}

export function Drawer({open,onClose,title,children,compact=false}:any){
  const reduce=useReducedMotion()
  return <Dialog.Root open={!!open} onOpenChange={e=>{if(!e.open)onClose?.()}} lazyMount unmountOnExit>
    <Portal>
      <Dialog.Backdrop className="v2-dialog-backdrop v2-drawer-backdrop"/>
      <Dialog.Positioner className="v2-drawer-positioner">
        <Dialog.Content className={`v2-drawer-content ${compact?'is-compact':''}`}>
          <motion.div className="v2-drawer-motion" initial={reduce?false:{opacity:0,x:24}} animate={{opacity:1,x:0}} transition={{duration:reduce?0:.22,ease:[.2,.8,.2,1]}}>
            <div className="v2-drawer-header">
              <Dialog.Title className="v2-dialog-title">{title}</Dialog.Title>
              <Dialog.CloseTrigger className="v2-icon-button" aria-label="Close"><Icon name="close"/></Dialog.CloseTrigger>
            </div>
            <div className="v2-drawer-body">{children}</div>
          </motion.div>
        </Dialog.Content>
      </Dialog.Positioner>
    </Portal>
  </Dialog.Root>
}

export type CommandItem={
  id:string
  label:string
  description?:string
  icon?:string
  keywords?:string
  action:()=>void
}

export function CommandPalette({open,onOpenChange,items}:{open:boolean,onOpenChange:(open:boolean)=>void,items:CommandItem[]}){
  const[query,setQuery]=React.useState('')
  const inputRef=React.useRef<HTMLInputElement|null>(null)
  React.useEffect(()=>{if(open){setQuery('');window.setTimeout(()=>inputRef.current?.focus(),40)}},[open])
  const normalized=query.trim().toLowerCase()
  const visible=normalized?items.filter(item=>`${item.label} ${item.description||''} ${item.keywords||''}`.toLowerCase().includes(normalized)):items
  const choose=(item:CommandItem)=>{item.action();onOpenChange(false)}
  return <Dialog.Root open={open} onOpenChange={e=>onOpenChange(e.open)} lazyMount unmountOnExit>
    <Portal>
      <Dialog.Backdrop className="v2-dialog-backdrop v2-command-backdrop"/>
      <Dialog.Positioner className="v2-command-positioner">
        <Dialog.Content className="v2-command-content">
          <Dialog.Title className="sr-only">Quick find</Dialog.Title>
          <Dialog.Description className="sr-only">Search pages, hosts, containers and common Vibewatch actions.</Dialog.Description>
          <div className="v2-command-search">
            <Icon name="search"/>
            <input ref={inputRef} value={query} onChange={e=>setQuery(e.target.value)} placeholder="Find pages, hosts, containers or actions…" onKeyDown={e=>{
              if(e.key==='Enter'&&visible[0]){e.preventDefault();choose(visible[0])}
            }}/>
            <kbd>Esc</kbd>
          </div>
          <div className="v2-command-list">
            {visible.slice(0,12).map(item=><button key={item.id} type="button" onClick={()=>choose(item)} className="v2-command-item">
              <span className="v2-command-icon"><Icon name={item.icon||'command'}/></span>
              <span className="min-w-0"><b>{item.label}</b>{item.description?<small>{item.description}</small>:null}</span>
              <kbd>↵</kbd>
            </button>)}
            {visible.length===0?<div className="v2-command-empty">No matching result.</div>:null}
          </div>
        </Dialog.Content>
      </Dialog.Positioner>
    </Portal>
  </Dialog.Root>
}

export function V2Tabs({value,onValueChange,items}:{value:string,onValueChange:(value:string)=>void,items:{value:string,label:string}[]}){
  return <Tabs.Root value={value} onValueChange={e=>onValueChange(e.value)} className="v2-tabs-root">
    <Tabs.List className="v2-tabs-list">
      {items.map(item=><Tabs.Trigger className="v2-tab-trigger" key={item.value} value={item.value}>{item.label}</Tabs.Trigger>)}
      <Tabs.Indicator className="v2-tab-indicator"/>
    </Tabs.List>
  </Tabs.Root>
}

export function V2Switch({checked,onCheckedChange,label,description,disabled=false}:{checked:boolean,onCheckedChange:(checked:boolean)=>void,label:string,description?:string,disabled?:boolean}){
  return <Switch.Root checked={checked} disabled={disabled} onCheckedChange={e=>onCheckedChange(e.checked)} className={`v2-switch-row ${disabled?'is-disabled':''}`}>
    <Switch.Label className="v2-switch-copy"><b>{label}</b>{description?<small>{description}</small>:null}</Switch.Label>
    <Switch.Control className="v2-switch-control"><Switch.Thumb className="v2-switch-thumb"/></Switch.Control>
    <Switch.HiddenInput/>
  </Switch.Root>
}
