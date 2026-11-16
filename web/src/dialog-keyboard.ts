// Native showModal owns inertness and Escape. Keep Tab at the modal edges in
// embedded browsers that otherwise let keyboard focus leave the document.
export function containDialogTab(event:KeyboardEvent) {
  if(event.key!=='Tab' || event.altKey || event.ctrlKey || event.metaKey)return
  const dialog=event.currentTarget
  if(!(dialog instanceof HTMLDialogElement) || !dialog.open)return
  const controls=[...dialog.querySelectorAll<HTMLElement>('a[href],button,input,select,textarea,[tabindex]')].filter(element=>element.tabIndex>=0 && !element.matches(':disabled') && !element.closest('[inert]') && element.getClientRects().length>0 && getComputedStyle(element).visibility!=='hidden')
  const first=controls[0],last=controls.at(-1),active=document.activeElement
  if(!first || !last){event.preventDefault();return}
  if(event.shiftKey && (active===first || !dialog.contains(active))){event.preventDefault();last.focus()}
  else if(!event.shiftKey && (active===last || !dialog.contains(active))){event.preventDefault();first.focus()}
}
