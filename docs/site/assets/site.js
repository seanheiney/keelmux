'use strict';
(() => {
  const base = document.currentScript.dataset.base;
  const dialog = document.querySelector('.search-dialog');
  const input = document.querySelector('#search-input');
  const results = document.querySelector('#search-results');
  const status = document.querySelector('#search-status');
  let indexPromise, lastTrigger;
  const index = () => indexPromise ||= fetch(base + 'search-index.json').then(r => {
    if (!r.ok) throw new Error('Search index unavailable');
    return r.json();
  });
  const openSearch = async trigger => {
    lastTrigger = trigger || document.activeElement;
    dialog.showModal(); input.focus();
    try { await index(); } catch { status.textContent = 'Search is unavailable. Use the guide navigation.'; }
  };
  const closeSearch = () => { dialog.close(); lastTrigger?.focus(); };
  document.querySelector('.search-trigger').addEventListener('click', e => openSearch(e.currentTarget));
  document.querySelector('.search-close').addEventListener('click', closeSearch);
  dialog.addEventListener('cancel', e => { e.preventDefault(); closeSearch(); });
  dialog.addEventListener('click', e => { if (e.target === dialog) { const r=dialog.getBoundingClientRect(); if(e.clientX<r.left||e.clientX>r.right||e.clientY<r.top||e.clientY>r.bottom) closeSearch(); } });
  input.addEventListener('input', async () => {
    const query = input.value.trim().toLowerCase(); results.replaceChildren();
    if (!query) { status.textContent = 'Type to search this documentation.'; return; }
    try {
      const pages = await index();
      if (input.value.trim().toLowerCase() !== query) return;
      const terms = query.split(/\s+/);
      const hits = pages.map(p => {
        const body=p.body.toLowerCase(),title=p.title.toLowerCase();
        const match=terms.every(t=>body.includes(t)||title.includes(t));
        return {p, score:match ? 1+terms.filter(t=>title.includes(t)).length*5 : 0};
      }).filter(x=>x.score).sort((a,b)=>b.score-a.score).slice(0,12);
      status.textContent = hits.length ? `${hits.length} guide${hits.length===1?'':'s'} found.` : 'No matching guides. Try fewer words.';
      for (const {p} of hits) {
        const li=document.createElement('li'),a=document.createElement('a'),group=document.createElement('span'),title=document.createElement('strong'),excerpt=document.createElement('p');
        a.href=p.url; group.textContent=p.group; title.textContent=p.title;
        const body=p.body.replace(/\s+/g,' '),offset=Math.max(0,body.toLowerCase().indexOf(terms[0])-45);
        excerpt.textContent=(offset?'…':'')+body.slice(offset,offset+155)+'…';
        a.append(group,title,excerpt);li.append(a);results.append(li);
      }
    } catch { status.textContent='Search is unavailable. Use the guide navigation.'; }
  });
  input.addEventListener('keydown',e=>{if(e.key==='ArrowDown'){const a=results.querySelector('a');if(a){e.preventDefault();a.focus();}}});
  document.addEventListener('keydown',e=>{
    const editing=/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement.tagName)||document.activeElement.isContentEditable;
    if(e.key==='/'&&!editing&&!dialog.open){e.preventDefault();openSearch();}
    if(e.key==='Escape'&&document.querySelector('.guide-nav.open')) closeMenu();
    if(e.key==='Tab'&&nav?.classList.contains('open')&&!dialog.open){
      const stops=[menu,...nav.querySelectorAll('a')],first=stops[0],last=stops[stops.length-1];
      if(e.shiftKey&&document.activeElement===first){e.preventDefault();last.focus();}
      else if(!e.shiftKey&&document.activeElement===last){e.preventDefault();first.focus();}
    }
  });
  document.querySelectorAll('.copy').forEach(button=>button.addEventListener('click',async()=>{
    const code=button.closest('.code-block').querySelector('pre code').textContent;
    try {await navigator.clipboard.writeText(code);button.textContent='Copied';document.querySelector('#copy-status').textContent='Code copied to clipboard.';setTimeout(()=>button.textContent='Copy',1800);}
    catch {button.textContent='Select code';document.querySelector('#copy-status').textContent='Clipboard unavailable. Select and copy the code manually.';}
  }));
  const menu=document.querySelector('.menu-trigger'),nav=document.querySelector('.guide-nav');
  const behindMenu=[document.querySelector('.article'),document.querySelector('.site-footer'),document.querySelector('.site-header .wordmark'),document.querySelector('.search-trigger')].filter(Boolean);
  function closeMenu(){nav?.classList.remove('open');menu?.setAttribute('aria-expanded','false');document.body.style.overflow='';behindMenu.forEach(el=>el.inert=false);menu?.focus();}
  menu?.addEventListener('click',()=>{
    if(!nav)return;
    const open=nav.classList.toggle('open');menu.setAttribute('aria-expanded',String(open));document.body.style.overflow=open?'hidden':'';
    behindMenu.forEach(el=>el.inert=open);
    if(open)nav.querySelector('[aria-current]')?.focus();
  });
  matchMedia('(min-width: 701px)').addEventListener('change',e=>{if(e.matches&&nav?.classList.contains('open'))closeMenu();});
  // Section tracking is reading orientation, not a telemetry channel.
  const headings=document.querySelectorAll('.article h2[id]');
  if('IntersectionObserver' in window){
    const observer=new IntersectionObserver(entries=>{
      for(const e of entries)if(e.isIntersecting){document.querySelectorAll('.toc a').forEach(a=>a.classList.toggle('current',a.hash==='#'+e.target.id));}
    },{rootMargin:'-90px 0px -65% 0px'});headings.forEach(h=>observer.observe(h));
  }
})();
