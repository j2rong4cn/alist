package quqi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alist-org/alist/v3/internal/driver"
	"github.com/alist-org/alist/v3/internal/errs"
	"github.com/alist-org/alist/v3/internal/model"
	"github.com/alist-org/alist/v3/pkg/utils"
	"github.com/go-resty/resty/v2"
	"github.com/tencentyun/cos-go-sdk-v5"
)

type Quqi struct {
	model.Storage
	Addition
	GroupID string
}

func (d *Quqi) Config() driver.Config {
	return config
}

func (d *Quqi) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Quqi) Init(ctx context.Context) error {
	// 登录
	if err := d.login(); err != nil {
		return err
	}

	// (暂时仅获取私人云) 获取私人云ID
	groupResp := &GroupRes{}
	if _, err := d.request("group.quqi.com", "/v1/group/list", resty.MethodGet, nil, groupResp); err != nil {
		return err
	}
	for _, groupInfo := range groupResp.Data {
		if groupInfo == nil {
			continue
		}
		if groupInfo.Type == 2 {
			d.GroupID = strconv.Itoa(groupInfo.ID)
			break
		}
	}
	if d.GroupID == "" {
		return errs.StorageNotFound
	}

	return nil
}

func (d *Quqi) Drop(ctx context.Context) error {
	return nil
}

func (d *Quqi) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var (
		listResp = &ListRes{}
		files    []model.Obj
	)

	if _, err := d.request("", "/api/dir/ls", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id": d.GroupID,
			"node_id": dir.GetID(),
		})
	}, listResp); err != nil {
		return nil, err
	}

	if listResp.Data == nil {
		return nil, nil
	}

	// dirs
	for _, dirInfo := range listResp.Data.Dir {
		if dirInfo == nil {
			continue
		}
		files = append(files, &model.Object{
			ID:       strconv.FormatInt(dirInfo.NodeID, 10),
			Name:     dirInfo.Name,
			Modified: time.Unix(dirInfo.UpdateTime, 0),
			Ctime:    time.Unix(dirInfo.AddTime, 0),
			IsFolder: true,
		})
	}

	// files
	for _, fileInfo := range listResp.Data.File {
		if fileInfo == nil {
			continue
		}
		if fileInfo.EXT != "" {
			fileInfo.Name = strings.Join([]string{fileInfo.Name, fileInfo.EXT}, ".")
		}

		files = append(files, &model.Object{
			ID:       strconv.FormatInt(fileInfo.NodeID, 10),
			Name:     fileInfo.Name,
			Size:     fileInfo.Size,
			Modified: time.Unix(fileInfo.UpdateTime, 0),
			Ctime:    time.Unix(fileInfo.AddTime, 0),
		})
	}

	return files, nil
}

func (d *Quqi) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var getDocResp = &GetDocRes{}

	if _, err := d.request("", "/api/doc/getDoc", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id": d.GroupID,
			"node_id": file.GetID(),
		})
	}, getDocResp); err != nil {
		return nil, err
	}

	return &model.Link{
		URL: getDocResp.Data.OriginPath,
		Header: http.Header{
			"Origin": []string{"https://quqi.com"},
			"Cookie": []string{d.Cookie},
		},
	}, nil
}

func (d *Quqi) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	var (
		makeDirRes = &MakeDirRes{}
		timeNow    = time.Now()
	)

	if _, err := d.request("", "/api/dir/mkDir", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id":   d.GroupID,
			"parent_id": parentDir.GetID(),
			"name":      dirName,
		})
	}, makeDirRes); err != nil {
		return nil, err
	}

	return &model.Object{
		ID:       strconv.FormatInt(makeDirRes.Data.NodeID, 10),
		Name:     dirName,
		Modified: timeNow,
		Ctime:    timeNow,
		IsFolder: true,
	}, nil
}

func (d *Quqi) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	var moveRes = &MoveRes{}

	if _, err := d.request("", "/api/dir/mvDir", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id":        d.GroupID,
			"node_id":        dstDir.GetID(),
			"source_quqi_id": d.GroupID,
			"source_node_id": srcObj.GetID(),
		})
	}, moveRes); err != nil {
		return nil, err
	}

	return &model.Object{
		ID:       strconv.FormatInt(moveRes.Data.NodeID, 10),
		Name:     moveRes.Data.NodeName,
		Size:     srcObj.GetSize(),
		Modified: time.Now(),
		Ctime:    srcObj.CreateTime(),
		IsFolder: srcObj.IsDir(),
	}, nil
}

func (d *Quqi) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	var renameRes = &RenameRes{}

	if _, err := d.request("", "/api/dir/renameDir", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id": d.GroupID,
			"node_id": srcObj.GetID(),
			"rename":  newName,
		})
	}, renameRes); err != nil {
		return nil, err
	}

	return &model.Object{
		ID:       strconv.FormatInt(renameRes.Data.NodeID, 10),
		Name:     renameRes.Data.Rename,
		Size:     srcObj.GetSize(),
		Modified: time.Unix(renameRes.Data.UpdateTime, 0),
		Ctime:    srcObj.CreateTime(),
		IsFolder: srcObj.IsDir(),
	}, nil
}

func (d *Quqi) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// 无法从曲奇接口响应中直接获取复制后的文件信息
	if _, err := d.request("", "/api/node/copy", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id":        d.GroupID,
			"node_id":        dstDir.GetID(),
			"source_quqi_id": d.GroupID,
			"source_node_id": srcObj.GetID(),
		})
	}, nil); err != nil {
		return nil, err
	}

	return nil, nil
}

func (d *Quqi) Remove(ctx context.Context, obj model.Obj) error {
	// 暂时不做直接删除，默认都放到回收站。直接删除方法：先调用删除接口放入回收站，在通过回收站接口删除文件
	if _, err := d.request("", "/api/node/del", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id": d.GroupID,
			"node_id": obj.GetID(),
		})
	}, nil); err != nil {
		return err
	}

	return nil
}

func (d *Quqi) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// base info
	sizeStr := strconv.FormatInt(stream.GetSize(), 10)
	f, err := stream.CacheFullInTempFile()
	if err != nil {
		return nil, err
	}
	md5, err := utils.HashFile(utils.MD5, f)
	if err != nil {
		return nil, err
	}
	sha, err := utils.HashFile(utils.SHA256, f)
	if err != nil {
		return nil, err
	}
	// init upload
	var uploadInitResp UploadInitResp
	_, err = d.request("", "/api/upload/v1/file/init", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"quqi_id":   d.GroupID,
			"tree_id":   "1",
			"parent_id": dstDir.GetID(),
			"size":      sizeStr,
			"file_name": stream.GetName(),
			"md5":       md5,
			"sha":       sha,
			"is_slice":  "true",
			"client_id": "quqipc_F8X2qOlSfF",
		})
	}, &uploadInitResp)
	if err != nil {
		return nil, err
	}
	// listParts
	_, err = d.request("upload.quqi.com:20807", "/upload/v1/listParts", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"token":     uploadInitResp.Data.Token,
			"task_id":   uploadInitResp.Data.TaskID,
			"client_id": "quqipc_F8X2qOlSfF",
		})
	}, nil)
	if err != nil {
		return nil, err
	}
	// get temp key
	var tempKeyResp TempKeyResp
	_, err = d.request("upload.quqi.com:20807", "/upload/v1/tempKey", resty.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(map[string]string{
			"token":   uploadInitResp.Data.Token,
			"task_id": uploadInitResp.Data.TaskID,
		})
	}, &tempKeyResp)
	if err != nil {
		return nil, err
	}
	// upload
	u, err := url.Parse(fmt.Sprintf("https://%s.cos.ap-shanghai.myqcloud.com", uploadInitResp.Data.Bucket))
	b := &cos.BaseURL{BucketURL: u}
	client := cos.NewClient(b, &http.Client{
		Transport: &cos.CredentialTransport{
			Credential: cos.NewTokenCredential(tempKeyResp.Data.Credentials.TmpSecretID, tempKeyResp.Data.Credentials.TmpSecretKey, tempKeyResp.Data.Credentials.SessionToken),
		},
	})
	partSize := int64(1024 * 1024 * 2)
	partCount := (stream.GetSize() + partSize - 1) / partSize
	for i := 1; i <= int(partCount); i++ {
		length := partSize
		if i == int(partCount) {
			length = stream.GetSize() - (int64(i)-1)*partSize
		}
		_, err := client.Object.UploadPart(
			context.Background(), uploadInitResp.Data.Key, uploadInitResp.Data.UploadID, i, io.LimitReader(f, partSize), &cos.ObjectUploadPartOptions{
				ContentLength: length,
			},
		)
		if err != nil {
			return nil, err
		}
	}
	//cfg := &aws.Config{
	//	Credentials: credentials.NewStaticCredentials(tempKeyResp.Data.Credentials.TmpSecretID, tempKeyResp.Data.Credentials.TmpSecretKey, tempKeyResp.Data.Credentials.SessionToken),
	//	Region:      aws.String("shanghai"),
	//	Endpoint:    aws.String("cos.ap-shanghai.myqcloud.com"),
	//	// S3ForcePathStyle: aws.Bool(true),
	//}
	//s, err := session.NewSession(cfg)
	//if err != nil {
	//	return nil, err
	//}
	//uploader := s3manager.NewUploader(s)
	//input := &s3manager.UploadInput{
	//	Bucket: &uploadInitResp.Data.Bucket,
	//	Key:    &uploadInitResp.Data.Key,
	//	Body:   f,
	//}
	//_, err = uploader.UploadWithContext(ctx, input)
	//if err != nil {
	//	return nil, err
	//}
	// finish upload
	var uploadFinishResp UploadFinishResp
	_, err = d.request("", "/api/upload/v1/file/finish", resty.MethodPost, func(req *resty.Request) {
		req.SetFormData(map[string]string{
			"token":     uploadInitResp.Data.Token,
			"task_id":   uploadInitResp.Data.TaskID,
			"client_id": "quqipc_F8X2qOlSfF",
		})
	}, &uploadFinishResp)
	if err != nil {
		return nil, err
	}
	return &model.Object{
		ID:       strconv.FormatInt(uploadFinishResp.Data.NodeID, 10),
		Name:     uploadFinishResp.Data.NodeName,
		Size:     stream.GetSize(),
		Modified: stream.ModTime(),
		Ctime:    stream.CreateTime(),
	}, nil
}

//func (d *Template) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*Quqi)(nil)