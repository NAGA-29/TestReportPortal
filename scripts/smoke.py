#!/usr/bin/env python3
"""Run against a running stack. Adds isolated smoke apps; never deletes user data."""
import base64
import json
import os
import unittest
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
if __name__=='__main__':unittest.main(verbosity=2)
