/*! CPA Management Center shared helpers
MIT License

Copyright (c) 2026 Router-For.ME

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
*/
var r=(t)=>typeof t==="string"?t.trim():"",g=(t)=>{let i=t.trim();if(i.length<=5)return i;return i.toLowerCase().endsWith(".json")?i.slice(0,-5):i},d=(t)=>{let i=r(t.name),e=g(i),n=r(t.email),l=r(t.projectId),s=n?"email":l?"projectId":"fileName",a=n||l||e,u=s==="fileName"||!e||e.toLowerCase()===a.toLowerCase()?null:e;return{primary:a,kind:s,secondary:u,fullName:i}};var y=new Map([["gemini models","group_gemini_models"],["claude and gpt models","group_claude_gpt_models"]]),m=new Map([["weekly limit","weekly_limit"],["daily limit","daily_limit"],["5 hour limit","five_hour_limit"],["5-hour limit","five_hour_limit"],["five hour limit","five_hour_limit"],["five-hour limit","five_hour_limit"],["monthly limit","monthly_limit"]]),c=new Map([["5h","five_hour_limit"],["five-hour","five_hour_limit"],["five_hour","five_hour_limit"],["daily","daily_limit"],["day","daily_limit"],["weekly","weekly_limit"],["week","weekly_limit"],["monthly","monthly_limit"],["month","monthly_limit"]]);var o=(t)=>t.trim().toLowerCase().replace(/\s+/g," "),p=(t)=>t.replace(/\s*(?:-|:)?\s*remaining$/,"").trim();function _(t){return y.get(o(t))??null}function h(t,i){let e=o(t),n=m.get(e)??m.get(p(e));if(n)return n;if(!i)return null;return c.get(o(i))??null}export{d as deriveAuthFileIdentity,h as resolveAntigravityBucketLabelKey,_ as resolveAntigravityGroupLabelKey};
