## ffblur

**f**ast **f**ind and **blur** image.

Requirements: ffmpeg.

<img align="right" src="https://user-images.githubusercontent.com/1284703/123284054-86221000-d53e-11eb-9d77-b30208df7605.png" width="250" />

If you submit videos containing politically-incorrect map to Chinese video
websites like Bilibili, your video may be rejected.

> 根据相关法律法规政策，您的视频中含有违禁内容（地图有误：藏南、阿克赛钦地区），
> 予以打回，请及时修改。让我们共同维护b站社区的健康氛围。

> According to relevant laws, regulations and policies, your video contains
> prohibited content (the map is incorrect: Southern Tibet, Aksai Chin Region)
> and is returned, please modify in time. Let us work together to maintain a
> healthy atmosphere in the Bilibili community.

You can use this utility to find those maps in the video and blur them.

<img src="https://user-images.githubusercontent.com/1284703/123286476-86bba600-d540-11eb-9aaa-ced3c0c45aeb.png" width="400" />

Usage:

1. Take screenshot in the video player. Crop the image, but don't resize it.
2. Run `ffblur -t map-1.jpg -t map-2.jpg -in original.ts -out modified.ts`

The process takes about 2 minutes for a 3.5hr-long video (about 6GB).
