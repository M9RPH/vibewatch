import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import './index.css'

const sendBrowserError=(message:string,stack='')=>{
  fetch('/api/client-errors',{
    method:'POST',
    headers:{'Content-Type':'application/json'},
    body:JSON.stringify({message,stack,url:window.location.href}),
  }).catch(()=>{})
}

window.addEventListener('error',event=>{
  sendBrowserError(event.message||'Unhandled browser error',event.error?.stack||'')
})
window.addEventListener('unhandledrejection',event=>{
  const reason=event.reason
  sendBrowserError(reason instanceof Error?reason.message:String(reason||'Unhandled promise rejection'),reason instanceof Error?reason.stack||'':'')
})

ReactDOM.createRoot(document.getElementById('root')!).render(<React.StrictMode><App/></React.StrictMode>)
