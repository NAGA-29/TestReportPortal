#!/usr/bin/env python3
"""Run against a running stack. Adds isolated smoke apps; never deletes user data."""
import base64
import json
import os
import unittest
import urllib.parse
import urllib.request
import urllib.error
import uuid
BASE = os.environ.get('LEDGER_URL', 'http://127.0.0.1:3000') + '/api'
def call(path, method='GET', body=None, expected=200):
    req = urllib.request.Request(BASE+path, method=method, data=None if body is None else json.dumps(body).encode(), headers={'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=15) as r:
            assert r.status == expected, (r.status,expected)
            return json.load(r)
    except urllib.error.HTTPError as e:
        if e.code != expected: raise AssertionError((e.code,e.read().decode())) from e
        return json.load(e)
class Workflow(unittest.TestCase):
    def test_ledger(self):
        self.assertEqual(call('/health')['status'],'ok')
        req=urllib.request.Request(BASE+'/apps',data=b'{"name":"forbidden"}',headers={'Content-Type':'application/json','Origin':'https://other.example'})
        with self.assertRaises(urllib.error.HTTPError) as caught: urllib.request.urlopen(req)
        self.assertEqual(caught.exception.code,403)
        app=call('/apps','POST',{'name':'Smoke '+uuid.uuid4().hex[:8]},201)
        path=f"/apps/{app['id']}"
        renamed=call(path,'PUT',{'name':' Renamed ','package_name':'com.example.smoke','description':'更新後'})
        self.assertEqual((renamed['id'],renamed['name'],renamed['package_name'],renamed['description']),(app['id'],'Renamed','com.example.smoke','更新後'))
        self.assertNotIn('current_version',renamed)
        call(path,'PUT',{'name':'x','current_version':'1.0.0'},400)
        call(path,'PUT',{'name':' '},400)
        call(path,'PUT',{'name':'x','unknown':1},400)
        call('/apps/999999999','PUT',{'name':'x'},404)
        definition=dict(case_key='LOGIN-001',title='ログイン',category='認証',precondition='',steps='ログインを押す',expected_result='ホーム画面',test_type='both',sort_order=0,is_active=True)
        case=call(path+'/cases','POST',definition,201); cid=case['id']
        call(path+'/cases','POST',definition,409)
        call(path+'/cases','POST',{**definition,'unknown':True},400)
        call(path+'/runs','POST',{'run_type':'manual','case_ids':[cid,cid]},400)
        other=call('/apps','POST',{'name':'Other '+uuid.uuid4().hex[:8]},201)
        call(f"/apps/{other['id']}/runs",'POST',{'run_type':'manual','case_ids':[cid]},400)
        first=call(path+'/runs','POST',dict(run_type='manual',case_ids=[cid],started_at='2026-01-01T00:00:00Z'),201)
        detail=lambda:call(f"/runs/{first['id']}")
        rid=detail()['results'][0]['id']
        call(f'/results/{rid}','PUT',{'status':'invalid'},400)
        call(f'/results/{rid}','PUT',{'status':'failed','comment':'接続エラー'})
        h=call(f'/results/{rid}/history')[0]
        self.assertEqual(h['before_data']['status'],'not_run')
        self.assertEqual(h['after_data']['status'],'failed')
        call(f'/results/{rid}','PUT',{'status':'passed'})
        call(f"/results/{rid}/restore/{h['id']}",'POST',{'reason':'誤編集の復元'})
        self.assertEqual(detail()['results'][0]['status'],'failed')
        self.assertEqual(call(f'/results/{rid}/history')[0]['action'],'restored')
        call(f'/cases/{cid}','PUT',{**definition,'title':'変更後のケース'})
        self.assertEqual(detail()['results'][0]['case_snapshot']['title'],'ログイン')
        second=call(path+'/runs','POST',dict(run_type='automated',runner_name='Maestro',case_ids=[cid],started_at='2026-01-02T00:00:00Z'),201)
        r2=call(f"/runs/{second['id']}")['results'][0]
        call(f"/results/{r2['id']}",'PUT',{'status':'passed'})
        call(f"/results/{r2['id']}/restore/{h['id']}",'POST',{},404)
        self.assertEqual([r['status'] for r in call(path+'/cases')[0]['recent_results']],['passed','failed'])
        self.assertEqual(len(call(f'/cases/{cid}/results')),2)
        image=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=')
        raw=b'--ledger\r\nContent-Disposition: form-data; name="file"; filename="screen.png"\r\nContent-Type: image/png\r\n\r\n'+image+b'\r\n--ledger--\r\n'
        req=urllib.request.Request(BASE+f'/results/{rid}/attachments',data=raw,headers={'Content-Type':'multipart/form-data; boundary=ledger'})
        with urllib.request.urlopen(req) as res:
            self.assertEqual(res.status,201); attachment=json.load(res)
        self.assertNotIn('storage_path',attachment)
        with urllib.request.urlopen(BASE+f"/attachments/{attachment['id']}") as res:
            self.assertEqual(res.read(),image)
        call(f'/cases/{cid}','PUT',{**definition,'is_active':False})
        call(path+'/runs','POST',dict(run_type='manual',case_ids=[cid]),400)
        self.assertEqual(len(call(path+'/runs')),2)
        self.assertEqual(call(f'/cases/{cid}/history')[0]['action'],'deactivated')
        print(f"Validated app={app['id']}, case={cid}, runs={first['id']},{second['id']}")
class CaseByKey(unittest.TestCase):
    def test_case_by_key(self):
        app=call('/apps','POST',{'name':'Smoke '+uuid.uuid4().hex[:8]},201)
        other=call('/apps','POST',{'name':'Other '+uuid.uuid4().hex[:8]},201)
        key='SYNC/001'; enc=urllib.parse.quote(key,safe='')
        path=f"/apps/{app['id']}/cases/by-key/{enc}"
        definition=dict(title='同期ケース',steps='開く',expected_result='表示',test_type='automated',changed_by='sync')
        call(path,expected=404)
        created=call(path,'PUT',definition,201)
        self.assertEqual(created['case_key'],key)
        self.assertEqual(call(path)['id'],created['id'])
        call(path,'PUT',definition)  # unchanged: no new history
        updated=call(path,'PUT',{**definition,'title':'同期ケース改'})
        self.assertEqual(updated['id'],created['id'])
        self.assertEqual([h['action'] for h in call(f"/cases/{created['id']}/history")],['updated','created'])
        call(path,'PUT',{**definition,'case_key':'OTHER'},400)
        call(path,'PUT',{**definition,'test_type':'invalid'},400)
        call(f"/apps/{other['id']}/cases/by-key/{enc}",expected=404)
        call(f"/apps/{other['id']}/cases/by-key/{enc}",'PUT',definition,201)
        self.assertEqual(call(path)['title'],'同期ケース改')
        call('/apps/999999999/cases/by-key/X',expected=404)
        # Surrounding whitespace and percent signs are part of the key and must not be trimmed or double-decoded.
        for raw in (' SPACED ','PCT%41'):
            p=f"/apps/{app['id']}/cases/by-key/"+urllib.parse.quote(raw,safe='')
            self.assertEqual(call(p,'PUT',definition,201)['case_key'],raw)
            self.assertEqual(call(p)['case_key'],raw)
        call(f"/apps/{app['id']}/cases/by-key/SPACED",expected=404)
class UpdateRun(unittest.TestCase):
    def test_update_run(self):
        app=call('/apps','POST',{'name':'Smoke '+uuid.uuid4().hex[:8]},201)
        case=call(f"/apps/{app['id']}/cases",'POST',dict(case_key='RUN-001',title='実行',steps='開く',expected_result='表示',test_type='automated'),201)
        run=call(f"/apps/{app['id']}/runs",'POST',dict(run_type='automated',case_ids=[case['id']],device_name='Pixel',started_at='2026-01-01T00:00:00Z'),201)
        self.assertIsNone(run['finished_at'])
        rpath=f"/runs/{run['id']}"
        call(rpath,'PATCH',{'finished_at':'2025-12-31T23:59:59Z'},400)
        call(rpath,'PATCH',{},400)
        call(rpath,'PATCH',{'unknown':1},400)
        call('/runs/999999999','PATCH',{'notes':'x'},404)
        updated=call(rpath,'PATCH',{'finished_at':'2026-01-01T00:30:00Z','summary':'完了'})
        self.assertTrue(updated['finished_at'].startswith('2026-01-01T00:30:00'))
        self.assertEqual(updated['device_name'],'Pixel')
        self.assertEqual(call(rpath)['summary'],'完了')
class ResultByKey(unittest.TestCase):
    def test_result_by_key(self):
        app=call('/apps','POST',{'name':'Smoke '+uuid.uuid4().hex[:8]},201)
        path=f"/apps/{app['id']}"
        base=dict(title='キー',steps='開く',expected_result='表示',test_type='automated')
        a=call(path+'/cases','POST',{**base,'case_key':'KEY/001'},201)
        b=call(path+'/cases','POST',{**base,'case_key':'KEY-002'},201)
        run=call(path+'/runs','POST',dict(run_type='automated',case_ids=[a['id']]),201)
        rpath=f"/runs/{run['id']}/results/by-key/"+urllib.parse.quote('KEY/001',safe='')
        call(f"/runs/{run['id']}/results/by-key/KEY-002",'PATCH',{'status':'passed'},404)
        call(f"/runs/999999999/results/by-key/KEY-002",'PATCH',{'status':'passed'},404)
        call(rpath,'PATCH',{},400)
        call(rpath,'PATCH',{'status':'invalid'},400)
        call(rpath,'PUT',{'status':'failed','actual_result':'エラー','comment':'初回','changed_by':'CI'})
        patched=call(rpath,'PATCH',{'comment':'再確認'})
        self.assertEqual((patched['status'],patched['actual_result'],patched['comment']),('failed','エラー','再確認'))
        history=call(f"/results/{patched['id']}/history")
        self.assertEqual([h['action'] for h in history],['updated','updated','created'])
        image=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=')
        raw=b'--ledger\r\nContent-Disposition: form-data; name="file"; filename="screen.png"\r\nContent-Type: image/png\r\n\r\n'+image+b'\r\n--ledger--\r\n'
        req=urllib.request.Request(BASE+rpath+'/attachments',data=raw,headers={'Content-Type':'multipart/form-data; boundary=ledger'})
        with urllib.request.urlopen(req) as res: self.assertEqual(res.status,201)
        self.assertEqual(len(call(f"/runs/{run['id']}")['results'][0]['attachments']),1)
        # A key with surrounding whitespace is distinct from the trimmed key and must address its own result.
        spaced=call(path+'/cases','POST',{**base,'case_key':' KEY-002 '},201)
        run2=call(path+'/runs','POST',dict(run_type='automated',case_ids=[b['id'],spaced['id']]),201)
        r=call(f"/runs/{run2['id']}/results/by-key/"+urllib.parse.quote(' KEY-002 ',safe=''),'PATCH',{'status':'blocked'})
        self.assertEqual(r['test_case_id'],spaced['id'])
        self.assertEqual(call(f"/runs/{run2['id']}/results/by-key/KEY-002",'PATCH',{'status':'passed'})['test_case_id'],b['id'])
def send(path, data, headers, expected):
    req=urllib.request.Request(BASE+path,method='POST',data=data,headers=headers)
    try:
        with urllib.request.urlopen(req,timeout=15) as r:
            assert r.status==expected,(r.status,expected)
            return json.load(r),r.headers
    except urllib.error.HTTPError as e:
        if e.code!=expected: raise AssertionError((e.code,e.read().decode())) from e
        return json.load(e),e.headers
class Idempotency(unittest.TestCase):
    def test_idempotency_key(self):
        app=call('/apps','POST',{'name':'Smoke '+uuid.uuid4().hex[:8]},201)
        path=f"/apps/{app['id']}"
        case=call(path+'/cases','POST',dict(case_key='IDEM-001',title='冪等',steps='開く',expected_result='表示',test_type='automated'),201)
        key=uuid.uuid4().hex
        body=json.dumps(dict(run_type='automated',case_ids=[case['id']])).encode()
        h={'Content-Type':'application/json','Idempotency-Key':key}
        first,_=send(path+'/runs',body,h,201)
        again,headers=send(path+'/runs',body,h,201)
        self.assertEqual(first['id'],again['id'])
        self.assertEqual(headers.get('Idempotent-Replayed'),'true')
        send(path+'/runs',json.dumps(dict(run_type='manual',case_ids=[case['id']])).encode(),h,409)
        self.assertEqual(len(call(path+'/runs')),1)
        failed=dict(h,**{'Idempotency-Key':uuid.uuid4().hex})
        send(path+'/runs',json.dumps(dict(run_type='automated',case_ids=[])).encode(),failed,400)
        send(path+'/runs',body,failed,201)  # failures do not consume the key
        rid=call(f"/runs/{first['id']}")['results'][0]['id']
        image=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII=')
        upload_key=uuid.uuid4().hex
        ids=[]
        for boundary in ('one','two'):  # retries may use a different boundary
            raw=f'--{boundary}\r\nContent-Disposition: form-data; name="file"; filename="screen.png"\r\nContent-Type: image/png\r\n\r\n'.encode()+image+f'\r\n--{boundary}--\r\n'.encode()
            att,_=send(f'/results/{rid}/attachments',raw,{'Content-Type':'multipart/form-data; boundary='+boundary,'Idempotency-Key':upload_key},201)
            ids.append(att['id'])
        self.assertEqual(ids[0],ids[1])
        # Field values containing separators must not collide with a different set of fields.
        def form(fields):
            parts=b''.join(f'--b\r\nContent-Disposition: form-data; name="{k}"\r\n\r\n{v}\r\n'.encode() for k,v in fields)
            return parts+b'--b\r\nContent-Disposition: form-data; name="file"; filename="screen.png"\r\nContent-Type: image/png\r\n\r\n'+image+b'\r\n--b--\r\n'
        mixed=uuid.uuid4().hex
        hb={'Content-Type':'multipart/form-data; boundary=b','Idempotency-Key':mixed}
        send(f'/results/{rid}/attachments',form([('caption','x'),('uploaded_by','y')]),hb,201)
        send(f'/results/{rid}/attachments',form([('caption','x\nv:uploaded_by=y')]),hb,409)
        self.assertEqual(len(call(f"/runs/{first['id']}")['results'][0]['attachments']),2)
def upload(path, name, content, expected=201):
    raw=f'--ledger\r\nContent-Disposition: form-data; name="file"; filename="{name}"\r\nContent-Type: application/octet-stream\r\n\r\n'.encode()+content+b'\r\n--ledger--\r\n'
    req=urllib.request.Request(BASE+path,data=raw,headers={'Content-Type':'multipart/form-data; boundary=ledger'})
    try:
        with urllib.request.urlopen(req,timeout=30) as r:
            assert r.status==expected,(r.status,expected); return json.load(r)
    except urllib.error.HTTPError as e:
        if e.code!=expected: raise AssertionError((e.code,e.read().decode())) from e
        return json.load(e)
class FileAttachments(unittest.TestCase):
    def test_file_attachments(self):
        app=call('/apps','POST',{'name':'Smoke '+uuid.uuid4().hex[:8]},201)
        case=call(f"/apps/{app['id']}/cases",'POST',dict(case_key='LOG-001',title='ログ',steps='開く',expected_result='表示',test_type='automated'),201)
        run=call(f"/apps/{app['id']}/runs",'POST',dict(run_type='automated',case_ids=[case['id']]),201)
        rid=call(f"/runs/{run['id']}")['results'][0]['id']
        path=f'/results/{rid}/attachments'
        log=b'11-24 10:00:00.000 E/AndroidRuntime: FATAL EXCEPTION: main\n'
        a=upload(path,'logcat.txt',log)
        self.assertEqual(a['mime_type'],'text/plain; charset=utf-8')
        self.assertEqual(upload(path,'flow.json',b'{"ok":true}')['mime_type'],'application/json')
        self.assertEqual(upload(path,'report.xml',b'<?xml version="1.0"?><testsuites/>')['mime_type'],'application/xml')
        h=upload(path,'page.log',b'<html><script>alert(1)</script></html>')
        self.assertEqual(h['mime_type'],'text/plain; charset=utf-8')  # never stored as HTML
        upload(path,'bad.json',b'{"ok":',400)
        upload(path,'page.html',b'<html></html>',400)
        upload(path,'tool.exe',b'MZ\x90\x00\x03\x00\x00\x00',400)
        upload(path,'empty.txt',b'',400)
        upload(path,'huge.log',b'a'*(20*1024*1024+1),413)
        with urllib.request.urlopen(BASE+f"/attachments/{a['id']}") as res:
            self.assertEqual(res.read(),log)
            self.assertTrue(res.headers['Content-Disposition'].startswith('attachment'))
            self.assertEqual(res.headers['X-Content-Type-Options'],'nosniff')
        self.assertEqual(len(call(f"/runs/{run['id']}")['results'][0]['attachments']),4)
if __name__=='__main__':unittest.main(verbosity=2)
